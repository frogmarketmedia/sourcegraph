package ui2

import (
	"bytes"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"

	"sourcegraph.com/sourcegraph/sourcegraph/cmd/frontend/internal/app/templates"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/handlerutil"
)

// TODO(slimsag): tests for everything in this file

// Functions that are exposed to templates.
var funcMap = template.FuncMap{
	"url": func(v ...string) string {
		return urlTo(v[0], v[1:]...).String()
	},
}

var (
	loadTemplateMu    sync.RWMutex
	loadTemplateCache = map[string]*template.Template{}
)

// loadTemplate loads the template with the given path. Also loaded along
// with that template is any templates under the shared/ directory.
func loadTemplate(path string) (*template.Template, error) {
	// Check the cache, first.
	loadTemplateMu.RLock()
	tmpl, ok := loadTemplateCache[path]
	loadTemplateMu.RUnlock()
	if ok && !handlerutil.DebugMode {
		return tmpl, nil
	}

	tmpl, err := doLoadTemplate(path, nil)
	if err != nil {
		return nil, err
	}

	// Update cache.
	loadTemplateMu.Lock()
	loadTemplateCache[path] = tmpl
	loadTemplateMu.Unlock()
	return tmpl, nil
}

// doLoadTemplate should only be called by loadTemplate.
func doLoadTemplate(path string, root *template.Template) (*template.Template, error) {
	// Determine template name.
	name := strings.TrimPrefix(path, "shared/")

	// Read the file.
	data, err := readFile(templates.Data, "ui2/"+path) // TODO(slimsag): remove ui2 in the future
	if err != nil {
		return nil, fmt.Errorf("ui: failed to read template %q: %v", path, err)
	}
	new := template.New
	if root != nil {
		new = root.New
	}
	tmpl, err := new(name).Funcs(funcMap).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("ui: failed to parse template %q: %v", path, err)
	}

	// If this is not a shared template itself, then load shared templates too.
	if !strings.HasPrefix(path, "shared") {
		for _, p := range mustListTemplates() {
			if strings.HasPrefix(p, "shared") {
				_, err = doLoadTemplate(p, tmpl)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return tmpl, nil
}

var (
	listTemplatesCache = []string{}
	listTemplatesOnce  sync.Once
)

// mustListTemplates returns a list of all template filepaths. If any error
// occurs, mustListTemplates panics.
func mustListTemplates() []string {
	onceOrDebug(&listTemplatesOnce, func() {
		var walk func(dir string) ([]string, error)
		walk = func(dir string) ([]string, error) {
			f, err := templates.Data.Open(dir)
			if err != nil {
				return nil, err
			}
			infos, err := f.Readdir(-1)
			if err != nil {
				return nil, err
			}
			var list []string
			for _, f := range infos {
				fp := path.Join(dir, f.Name())

				// Descend into further directories.
				if f.IsDir() {
					subList, err := walk(fp)
					if err != nil {
						return nil, err
					}
					list = append(list, subList...)
					continue
				}

				if !strings.HasSuffix(fp, ".html") {
					continue
				}
				fp = strings.TrimPrefix(fp, "ui2/") // TODO(slimsag): remove line in the future
				list = append(list, fp)
			}
			return list, nil
		}
		var err error
		listTemplatesCache, err = walk("ui2") // TODO(slimsag): replace with root in the future
		if err != nil {
			log.Println("ui: listing templates failed:", err)
			panic(err)
		}
	})
	return listTemplatesCache
}

func init() {
	// Kick off template loading initially in the background, so that any
	// template error causes a panic before a user request (to avoid a broken
	// build from going unnoticed).
	for _, path := range mustListTemplates() {
		_, err := loadTemplate(path)
		if err != nil {
			panic(fmt.Errorf("ui: failed to load template %q error: %s", path, err))
		}
	}
}

// renderTemplate renders the template with the given name. The template name
// is its file name, relative to the template directory.
//
// The given data is accessible in the template via $.Foobar
func renderTemplate(w http.ResponseWriter, name string, data interface{}) error {
	root, err := loadTemplate(name)
	if err != nil {
		return err
	}

	// Write to a buffer to avoid a partially written response going to w
	// when an error would occur. Otherwise, our error page template rendering
	// will be corrupted.
	var buf bytes.Buffer
	if err := root.Execute(&buf, data); err != nil {
		return err
	}
	_, err = buf.WriteTo(w)
	return err
}

// onceOrDebug invokes f() if running in debug mode, otherwise it just
// invokes o.Do(f)
func onceOrDebug(o *sync.Once, f func()) {
	if handlerutil.DebugMode {
		f()
		return
	}
	o.Do(f)
}

// readFile is like ioutil.ReadFile but for a http.FileSystem.
func readFile(fs http.FileSystem, path string) ([]byte, error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, err
	}
	return ioutil.ReadAll(f)
}
