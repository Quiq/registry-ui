package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/CloudyKit/jet"
	"github.com/labstack/echo/v4"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/tidwall/gjson"
)

// Template Jet template.
type Template struct {
	View *jet.Set
}

// Render render template.
func (r *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	t, err := r.View.GetTemplate(name)
	if err != nil {
		panic(fmt.Errorf("Fatal error template file: %s", err))
	}
	vars, ok := data.(jet.VarMap)
	if !ok {
		vars = jet.VarMap{}
	}
	err = t.Execute(w, vars, nil)
	if err != nil {
		panic(fmt.Errorf("Error rendering template %s: %s", name, err))
	}
	return nil
}

// setupRenderer template engine init.
func setupRenderer(debug bool, registryHost, basePath string) *Template {
	view := jet.NewHTMLSet("templates")
	view.SetDevelopmentMode(debug)

	view.AddGlobal("version", version)
	view.AddGlobal("basePath", basePath)
	view.AddGlobal("registryHost", registryHost)
	view.AddGlobal("pretty_size", func(size interface{}) string {
		var value float64
		switch i := size.(type) {
		case gjson.Result:
			value = float64(i.Int())
		case int64:
			value = float64(i)
		}
		return registry.PrettySize(value)
	})
	view.AddGlobal("pretty_time", func(datetime interface{}) string {
		d := strings.Replace(datetime.(string), "T", " ", 1)
		d = strings.Replace(d, "Z", "", 1)
		return strings.Split(d, ".")[0]
	})
	view.AddGlobal("parse_map", func(m interface{}) string {
		var res string
		for _, k := range registry.SortedMapKeys(m) {
			res = res + fmt.Sprintf(`<tr><td style="padding: 0 10px; width: 20%%">%s</td><td style="padding: 0 10px">%v</td></tr>`, k, m.(map[string]interface{})[k])
		}
		return res
	})
	view.AddGlobal("url_decode", func(m interface{}) string {
		res, err := url.PathUnescape(m.(string))
		if err != nil {
			return m.(string)
		}
		return res
	})

	return &Template{View: view}
}
