package main

import (
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

// blockedPrefixes lists special-purpose ranges that IsGlobalUnicast() does not exclude.
var blockedPrefixes = []netip.Prefix{
  netip.MustParsePrefix("0.0.0.0/8"),
  netip.MustParsePrefix("100.64.0.0/10"),   // Shared address space (CGNAT)
  netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 (may embed private IPv4)
  netip.MustParsePrefix("64:ff9b:1::/48"),  // Local-use NAT64
  netip.MustParsePrefix("2002::/16"),       // 6to4 (may embed private IPv4)
}

func connect(ctx context.Context, ch chan error, host string, port string) {
  log.Printf("Connecting to: %s (%s)", host, port)

  dialer := &net.Dialer{
    Control: func(network, address string, _ syscall.RawConn) error {
      ap, err := netip.ParseAddrPort(address)
      if err != nil {
        return err
      }

      addr := ap.Addr().Unmap() // Normalize IPv4-mapped IPv6 (::ffff:a.b.c.d)
      if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified() {
        return fmt.Errorf("forbidden address: %s", ap.Addr())
      }

      for _, p := range blockedPrefixes {
        if p.Contains(addr) {
          return fmt.Errorf("address is in blocked range: %s", ap.Addr())
        }
      }

      return nil
    },
  }

  con, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
  if err == nil {
    defer con.Close()
  } else {
    log.Println(err)
  }

  ch <- err
}

func gethost(query string) string {
  host := query

  // If the query can be parsed as an URI, use host part as a target host
  uri, err := url.Parse(host)
  if err == nil && uri.IsAbs() { // IsAbs must be true because a relative URI does not have a host part.
    urihost := uri.Hostname()
    if urihost != "" {
      log.Printf("URI-like string found, use '%s' as a hostname.", urihost)
      host = urihost
    }
  }

  // If the host is exactly IP address, use it as a target host
  ip := net.ParseIP(host)
  if ip != nil {
    return ip.String()
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

  return host
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

  host := gethost(query)
  if host == "" {
    http.Error(w, `Bad Request`, http.StatusBadRequest)
    return
  }


  // Cancelled on timeout, client disconnect, or handler return
  ctx, cancel := context.WithTimeout(r.Context(), 3 * time.Second)
  defer cancel()

  ch := make(chan error, 2)

  go connect(ctx, ch, host, "80")
  go connect(ctx, ch, host, "443")

  status := "unreachable"

  for i := 0; i < 2; i++ {
    select {
    case err := <-ch:
      if err == nil {
        status = "reachable"
        i = 2 // quit
      }
    case <-ctx.Done():
      log.Printf("Connection aborted: %v", ctx.Err())
      i = 2 // quit
    }
  }


  // IPv6 literals must be enclosed in brackets in a URL
  // gethost() only allows ':' in IPv6 addresses, so this check is sufficient
  urlhost := host
  if strings.Contains(host, ":") {
    urlhost = "[" + host + "]"
  }
  link := &url.URL{Scheme: "http", Host: urlhost, Path: "/"}

  data := &Render {
    Host:   host,
    URL:    link.String(),
    Status: status,
  }

  err := tmpl.Execute(w, data)
  if err != nil {
    log.Println(err)
  }
  return
}
