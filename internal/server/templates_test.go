package server

import (
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	templateDefine   = regexp.MustCompile(`(?s)\{\{define "([^"]+)"\}\}(.*?)(\{\{define "|\z)`)
	templateBalanced = []string{"a", "article", "aside", "button", "details", "div", "dl", "fieldset", "form", "header", "label", "main", "nav", "ol", "section", "select", "summary", "table", "textarea", "ul"}
)

// unbalancedDefines lists "define tag opens closes" for every template define whose
// opening and closing tags differ in number. A merge that keeps two versions of one
// tag, or drops a closing tag, shows up here.
func unbalancedDefines(t *testing.T) []string {
	t.Helper()
	var found []string
	err := fs.WalkDir(templates, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		text, err := fs.ReadFile(templates, path)
		if err != nil {
			return err
		}
		rest := string(text)
		for {
			match := templateDefine.FindStringSubmatchIndex(rest)
			if match == nil {
				break
			}
			name, body := rest[match[2]:match[3]], rest[match[4]:match[5]]
			for _, tag := range templateBalanced {
				opens := len(regexp.MustCompile(`<`+tag+`[\s>]`).FindAllString(body, -1))
				closes := strings.Count(body, "</"+tag+">")
				if opens != closes {
					found = append(found, name+" "+tag)
				}
			}
			rest = rest[match[5]:]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	return found
}

func TestTemplateTagsBalance(t *testing.T) {
	// These defines are halves of one element by design: each opens what its partner closes.
	want := []string{"card-filter-close details", "card-filter-open details", "layout-close div", "layout-close main", "layout-open div", "layout-open main"}
	if got := unbalancedDefines(t); !reflect.DeepEqual(got, want) {
		t.Errorf("defines with unbalanced tags = %v; want only the paired halves %v", got, want)
	}
}
