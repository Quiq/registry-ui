package main

import (
	"fmt"
	"io"
	"time"

	"github.com/CloudyKit/jet/v6"
	"github.com/labstack/echo/v4"
	"github.com/quiq/registry-ui/registry"
	"github.com/spf13/viper"
)

// Template Jet template.
type Template struct {
	View *jet.Set
}

// Render render template.
func (r *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	t, err := r.View.GetTemplate(name)
	if err != nil {
		panic(fmt.Errorf("fatal error template file: %s", err))
	}
	vars, ok := data.(jet.VarMap)
	if !ok {
		vars = jet.VarMap{}
	}
	err = t.Execute(w, vars, nil)
	if err != nil {
		panic(fmt.Errorf("error rendering template %s: %s", name, err))
	}
	return nil
}

// setupRenderer template engine init.
func setupRenderer(basePath string) *Template {
	var opts []jet.Option
	if viper.GetBool("debug.templates") {
		opts = append(opts, jet.InDevelopmentMode())
	}
	view := jet.NewSet(jet.NewOSFileSystemLoader("templates"), opts...)

	view.AddGlobal("version", version)
	view.AddGlobal("basePath", basePath)
	view.AddGlobal("registryHost", viper.GetString("registry.hostname"))
	view.AddGlobal("pretty_size", func(val interface{}) string {
		var s float64
		switch i := val.(type) {
		case int64:
			s = float64(i)
		case float64:
			s = i
		default:
			fmt.Printf("Unhandled type when calling pretty_size(): %T\n", i)
		}
		return registry.PrettySize(s)
	})
	view.AddGlobal("pretty_time", func(val interface{}) string {
		var t time.Time
		switch i := val.(type) {
		case string:
			var err error
			t, err = time.Parse("2006-01-02T15:04:05Z", i)
			if err != nil {
				// mysql case
				t, _ = time.Parse("2006-01-02 15:04:05", i)
			}
		default:
			t = i.(time.Time)
		}
		return t.In(time.Local).Format("2006-01-02 15:04:05 MST")
	})
	view.AddGlobal("sort_map_keys", func(m interface{}) []string {
		return registry.SortedMapKeys(m)
	})
	return &Template{View: view}
}
