package signoz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"text/template"

	"github.com/gliderlabs/logspout/router"
)

func init() {
	router.AdapterFactories.Register(NewSignozAdapter, "signoz")
}

var funcs = template.FuncMap{
	"toJSON": func(value interface{}) string {
		bytes, err := json.Marshal(value)
		if err != nil {
			log.Println("error marshaling to JSON: ", err)
			return "null"
		}
		return string(bytes)
	},
}

// NewSignozAdapter returns a configured signoz.Adapter
func NewSignozAdapter(route *router.Route) (router.LogAdapter, error) {
	//transport, found := router.AdapterTransports.Lookup(route.AdapterTransport("udp"))
	//if !found {
	//	return nil, errors.New("bad transport: " + route.Adapter)
	//}
	//conn, err := transport.Dial(route.Address, route.Options)
	//if err != nil {
	//	return nil, err
	//}
	tmplStr := "{{toJSON .}}\n"
	if os.Getenv("RAW_FORMAT") != "" {
		tmplStr = os.Getenv("RAW_FORMAT")
	}
	tmpl, err := template.New("signoz").Funcs(funcs).Parse(tmplStr)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		route: route,
		//conn:  conn,
		tmpl: tmpl,
	}, nil
}

// Adapter is a simple adapter that streams log output to a connection without any templating
type Adapter struct {
	//conn  net.Conn
	route *router.Route
	tmpl  *template.Template
}

// Stream sends log data to a connection
func (a *Adapter) Stream(logstream chan *router.Message) {
	for message := range logstream {
		buf := new(bytes.Buffer)
		err := a.tmpl.Execute(buf, message)
		if err != nil {
			log.Println("signoz 123:", err)
			return
		}
		fmt.Println("%s", buf.Bytes())
	}
}
