package main

import (
  "bytes"
  "context"
  "fmt"
  "html/template"
  "log"
  "net"
  "net/http"
  "net/netip"
  "net/url"
  "os"
  "strings"
  "syscall"
  "time"
)

type Render struct {
  Host   string
  URL    string
  Status string
}

var tmpl = template.Must(template.ParseFiles("template.html"))
var checkPorts = []string{"80", "443"}

// blockedPrefixes lists special-purpose ranges that IsGlobalUnicast() does not exclude
var blockedPrefixes = []netip.Prefix{
  netip.MustParsePrefix("0.0.0.0/8"),
  netip.MustParsePrefix("100.64.0.0/10"),   // Shared address space (CGNAT)
  netip.MustParsePrefix("2001::/32"),       // Teredo (may embed IPv4)
  netip.MustParsePrefix("2002::/16"),       // 6to4 (may embed private IPv4)
  netip.MustParsePrefix("64:ff9b:1::/48"),  // Local-use NAT64
  netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 (may embed private IPv4)
}

var dialer = &net.Dialer{
  Control: func(_, address string, _ syscall.RawConn) error {
    ap, err := netip.ParseAddrPort(address)
    if err != nil {
      return err
    }
    return validateAddr(ap.Addr())
  },
}

func validateAddr(a netip.Addr) error {
  addr := a.Unmap() // Normalize IPv4-mapped IPv6 (::ffff:a.b.c.d)
  if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified() {
    return fmt.Errorf("forbidden address: %s", a)
  }

  for _, p := range blockedPrefixes {
    if p.Contains(addr) {
      return fmt.Errorf("address is in blocked range: %s", a)
    }
  }
  return nil
}

func connect(ctx context.Context, host, port string) error {
  log.Printf("Connecting to: %s (%s)", host, port)

  con, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
  if err != nil {
    log.Println(err)
    return err
  }
  return con.Close()
}

func checkReachable(ctx context.Context, host string) bool {
  // Cancelled on timeout, client disconnect, or handler return
  ctx, cancel := context.WithTimeout(ctx, 3 * time.Second)
  defer cancel() // Aborts remaining dials once a result is determined

  ch := make(chan error, len(checkPorts)) // Buffered so goroutines never block
  for _, port := range checkPorts {
    go func(port string) {
      ch <- connect(ctx, host, port)
    }(port)
  }

  for range checkPorts {
    select {
    case err := <-ch:
      if err == nil {
        return true
      }
    case <-ctx.Done():
      log.Printf("Connection aborted: %v", ctx.Err())
      return false
    }
  }
  return false
}

func getHost(query string) string {
  host := strings.TrimSpace(query)

  // If the host is exactly IP address, use it as a target host
  addr, err := netip.ParseAddr(host)
  if err == nil {
    if addr.Zone() != "" {
      return "" // Reject IPv6 zone identifiers (e.g. fe80::1%eth0)
    }
    return addr.String()
  }

  // If the query can be parsed as an URI, use host part as a target host
  uri, err := url.Parse(host)
  if err == nil && uri.IsAbs() { // A relative URI does not have a host part
    urihost := uri.Hostname()
    if urihost != "" {
      log.Printf("URI-like string found, use '%s' as a hostname.", urihost)
      host = urihost
    }
  }

  // Check whether the string contains any invalid characters for a hostname
  // (It just checks only for the characters, the validity of hostname will be checked during DNS name resolution)
  if host == "" || len(host) > 255 {
    return "" // DNS name must be less than 255 characters (https://tools.ietf.org/html/rfc1035#section-2.3.4)
  }

  for _, r := range host {
    if !(('0' <= r && r <= '9') || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') || r == '-' || r == '.') {
      return ""
    }
  }

  return strings.TrimSuffix(host, ".")
}

func main() {
  mux := http.NewServeMux()
  mux.HandleFunc("/check", handler)

  port := os.Getenv("PORT")
  if port == "" {
    port = "8080"
    log.Printf("Defaulting to port %s", port)
  }


  server := &http.Server{
    Addr:              ":" + port,
    Handler:           mux,
    ReadHeaderTimeout: 10 * time.Second, // Mitigate Slowloris
    ReadTimeout:       10 * time.Second,
    WriteTimeout:      10 * time.Second, // Must exceed the 3s check timeout
    IdleTimeout:       60 * time.Second,
  }

  log.Printf("Listening on port %s", port)
  err := server.ListenAndServe()
  if err != nil {
    log.Fatal(err)
  }
}

func handler(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodGet {
    w.Header().Set("Allow", "GET")
    http.Error(w, `Method Not Allowed`, http.StatusMethodNotAllowed)
    return
  }

  query := r.URL.Query().Get("q")
  if query == "" {
    http.Redirect(w, r, "/", http.StatusFound)
    return
  }

  host := getHost(query)
  if host == "" {
    http.Error(w, `Bad Request`, http.StatusBadRequest)
    return
  }

  status := "unreachable"
  if checkReachable(r.Context(), host) {
    status = "reachable"
  }


  // IPv6 literals must be enclosed in brackets in a URL
  // getHost() only allows ':' in IPv6 addresses, so this check is sufficient
  urlhost := host
  if strings.Contains(host, ":") {
    urlhost = "[" + host + "]"
  }
  link := &url.URL{Scheme: "http", Host: urlhost, Path: "/"}

  data := &Render{
    Host:   host,
    URL:    link.String(),
    Status: status,
  }


  var buf bytes.Buffer
  err := tmpl.Execute(&buf, data)
  if err != nil {
    log.Println(err)
    http.Error(w, `Internal Server Error`, http.StatusInternalServerError)
    return
  }

  w.Header().Set("X-Content-Type-Options", "nosniff")
  w.Header().Set("Referrer-Policy", "no-referrer")
  _, err = buf.WriteTo(w)
  if err != nil {
    log.Println(err)
  }
}
