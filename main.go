package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

type Mod map[string]string

const indexFile = "mod_index.json"

var modAttributes = []string{"Name", "Version", "Author", "Guid", "File"}

func isModAttribute(s string) bool {
	for _, a := range modAttributes {
		if a == s {
			return true
		}
	}
	return false
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func loadPreviousMods() []Mod {
	data, err := os.ReadFile(indexFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("Previous data doesn't exist")
			return []Mod{}
		}
		fatal("Failed to load previous version", "err", err)
	}

	var mods []Mod
	if err := json.Unmarshal(data, &mods); err != nil {
		fatal("Failed to load previous version", "err", err)
	}
	return mods
}

func saveMods(newMods, prevMods []Mod) {
	mods := append(append([]Mod{}, newMods...), prevMods...)

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false) // equivalent of ensure_ascii=False for <, >, &
	enc.SetIndent("", "  ")
	if err := enc.Encode(mods); err != nil {
		fatal("Failed to save mods", "err", err)
	}

	if err := os.WriteFile(indexFile, []byte(sb.String()), 0o644); err != nil {
		fatal("Failed to save mods", "err", err)
	}
	slog.Info("Mods saved successfully")
}

func outerHTML(s *goquery.Selection) string {
	h, err := goquery.OuterHtml(s)
	if err != nil {
		return ""
	}
	return h
}

// nodeText returns the text content of a node (like BeautifulSoup's .text).
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func getMod(anchor *goquery.Selection) Mod {
	mod := Mod{}
	parentDiv := anchor.Find("div.mod-library-item-grid").First()

	parentDiv.Find("div").Each(func(_ int, div *goquery.Selection) {
		attrElm := div.Find("span.eyebrow").First()
		if attrElm.Length() == 0 {
			return
		}
		attribute := nodeText(attrElm.Nodes[0])
		if !isModAttribute(attribute) {
			return
		}

		// First child node (element or text) that isn't the eyebrow span.
		for c := div.Nodes[0].FirstChild; c != nil; c = c.NextSibling {
			if c == attrElm.Nodes[0] {
				continue
			}
			mod[attribute] = nodeText(c)
			break
		}
	})

	if len(mod) != len(modAttributes) {
		var missing []string
		for _, a := range modAttributes {
			if _, ok := mod[a]; !ok {
				missing = append(missing, a)
			}
		}
		sort.Strings(missing)
		fatal(fmt.Sprintf("Failed to parse mod. Missing %v.", missing), "anchor", outerHTML(anchor))
	}

	href, _ := anchor.Attr("href")
	mod["Link"] = href
	if mod["Link"] == "" || mod["Guid"] == "" {
		fatal("Failed to parse mod: Missing link and/or guid.", "anchor", outerHTML(anchor))
	}
	return mod
}

func fetchPage(page int) *goquery.Document {
	url := fmt.Sprintf("https://koikatsucards.com/mod_library?page=%d", page)
	res, err := http.Get(url)
	if err != nil {
		fatal("Request failed", "url", url, "err", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fatal("Failed to read response", "url", url, "err", err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		fatal("Failed to parse HTML", "url", url, "err", err)
	}
	return doc
}

func main() {
	newMods := []Mod{}
	prevMods := loadPreviousMods()

	var endMod Mod
	if len(prevMods) > 0 {
		endMod = prevMods[0]
	}

	for page := 1; ; page++ {
		doc := fetchPage(page)
		anchors := doc.Find("a.mod-library-item-link")

		if anchors.Length() == 0 {
			slog.Info("No more mods found")
			saveMods(newMods, prevMods)
			return
		}

		reached := false
		anchors.EachWithBreak(func(_ int, a *goquery.Selection) bool {
			mod := getMod(a)
			if endMod != nil && reflect.DeepEqual(mod, endMod) {
				slog.Info("Previous start mod reached")
				reached = true
				return false
			}
			newMods = append(newMods, mod)
			return true
		})

		if reached {
			saveMods(newMods, prevMods)
			return
		}
	}
}