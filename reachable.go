package main

import (
  "fmt"
  "html/template"
  "log"
  "net"
  "net/http"
  "net/url"
  "os"
  "syscall"
  "time"
)

type Render struct {
  Host   string
  Status string
}

func connect(ch chan error, host string, port string) {
  log.Printf("Connecting to: %s (%s)", host, port)

  dialer := &net.Dialer{
    Timeout: 5 * time.Second, // Set hard timeout: 5 seconds
    Control: func(network, address string, _ syscall.RawConn) error {
      host, _, err := net.SplitHostPort(address)
      if err != nil {
        return err
      }

      ip := net.ParseIP(host)
      if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
        return fmt.Errorf("forbidden address: %s", ip.String())
      }
      return nil
    },
  }

  con, err := dialer.Dial("tcp", net.JoinHostPort(host, port))
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
  http.HandleFunc("/check", handler)

  port := os.Getenv("PORT")
  if port == "" {
    port = "8080"
    log.Printf("Defaulting to port %s", port)
  }

  log.Printf("Listening on port %s", port)
  err := http.ListenAndServe(":" + port, nil)
  if err != nil {
    log.Fatal(err)
  }
}

func handler(w http.ResponseWriter, r *http.Request) {
  if r.Method != "GET" {
    http.Error(w, `Bad Request`, http.StatusBadRequest)
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


  ch := make(chan error, 2)

  go connect(ch, host, "80")
  go connect(ch, host, "443")

  status := "unreachable"
  timeout := time.After(3 * time.Second) // Set soft timeout: 3 seconds

  for i := 0; i < 2; i++ {
    select {
    case err := <-ch:
      if err == nil {
        status = "reachable"
        i = 2 // quit
      }
    case <-timeout:
      log.Println("Connection timeout!!")
      i = 2 // quit
    }
  }

  data := &Render {
    Host:   host,
    Status: status,
  }
  template.Must(template.ParseFiles("template.html")).Execute(w, data)
  return
}
