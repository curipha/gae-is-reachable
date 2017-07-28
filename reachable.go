package reachable

import (
  "time"
  "html/template"
  "net/http"
  "net/url"

  "google.golang.org/appengine"
  "google.golang.org/appengine/urlfetch"
)

type Render struct {
  Host   string
  Status string
}

func fetch(r *http.Request, ch chan error, uri string) {
  ct := appengine.NewContext(r)
  tr := &urlfetch.Transport {
    Context: ct,
    AllowInvalidServerCertificate: true, // Accept invalid certificate over HTTPS connection
  }
  req, _ := http.NewRequest("HEAD", uri, nil)
  _, err := tr.RoundTrip(req)

  ch <- err
}

func gethost(query string) string {
  uri, err := url.Parse(query)

  if err == nil {
    return uri.Hostname()
  }

  return ""
}

func init() {
  http.HandleFunc("/check", handler)
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

  go fetch(r, ch, "http://" + host + "/") // TODO: `host` variable may contain other than hostname
  go fetch(r, ch, "https://" + host + "/")

  status := "reachable"
  select {
  case result := <-ch:
    if result != nil {
      // Failure ... detailed errro can show by: result.Error()
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
