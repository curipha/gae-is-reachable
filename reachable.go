package main

import (
  "html/template"
  "log"
  "net"
  "net/http"
  "net/url"
  "os"
  "time"
)

type Render struct {
  Host   string
  Status string
}

func connect(ch chan error, host string, port string) {
  con, err := net.Dial("tcp", net.JoinHostPort(host, port))
  if err == nil {
    defer con.Close()
  } else {
    log.Println(err)
  }

  ch <- err
}

func gethost(query string) string {
  uri, err := url.Parse(query)

  if err == nil {
    return uri.Hostname()
  }

  return ""
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

  if len(query) < 1 {
    http.Redirect(w, r, "/", http.StatusFound)
    return
  }


  host := gethost(query)  // Support the query of URI-formatted string

  if len(host) < 1 {
    host = gethost("http://" + query)
  }

  if len(host) < 1 {
    http.Error(w, `Bad Request`, http.StatusBadRequest)
    return
  }


  ch := make(chan error, 1)

  go connect(ch, host, "80")
  go connect(ch, host, "443")

  status := "reachable"
  select {
  case result := <-ch:
    if result != nil {
      // Failure
      status = "unreachable"
    }
  case <- time.After(time.Second * 3):
    // Timeout
    status = "unreachable"
  }

  data := &Render {
    Host:   host,
    Status: status,
  }
  template.Must(template.ParseFiles("template.html")).Execute(w, data)
  return
}
