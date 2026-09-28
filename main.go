package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

type Mod map[string]string

const indexFile = "kkc_mod_index.json"

var modAttributes = []string{"Name", "Version", "Author", "Guid", "File"}

var httpClient = &http.Client{Timeout: 30 * time.Second}

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

// ---------- git ----------

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func commitChanges(message string) {
	if out, err := git("add", "."); err != nil {
		fatal("git add failed", "err", err, "output", out)
	}

	// `git commit` exits non-zero when there's nothing to commit,
	// so check first to avoid treating that as a failure.
	status, err := git("status", "--porcelain")
	if err != nil {
		fatal("git status failed", "err", err, "output", status)
	}
	if status == "" {
		slog.Info("Nothing to commit")
		return
	}

	if out, err := git("commit", "-m", message); err != nil {
		fatal("git commit failed", "err", err, "output", out)
	}
	slog.Info("Changes committed")

	if out, err := git("push"); err != nil {
		fatal("git push failed", "err", err, "output", out)
	}
	slog.Info("Changes pushed")
}

// ---------- persistence ----------

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
	enc.SetEscapeHTML(false) // keep <, >, & unescaped
	enc.SetIndent("", "  ")
	if err := enc.Encode(mods); err != nil {
		fatal("Failed to save mods", "err", err)
	}

	if err := os.WriteFile(indexFile, []byte(sb.String()), 0o644); err != nil {
		fatal("Failed to save mods", "err", err)
	}
	slog.Info("Mods saved successfully")

	commitChanges(fmt.Sprintf("Update mod index (%d new mods)", len(newMods)))
}

// ---------- parsing ----------

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

// ---------- fetching ----------

func fetchPage(page int) *goquery.Document {
	url := fmt.Sprintf("https://koikatsucards.com/mod_library?page=%d", page)
	res, err := httpClient.Get(url)
	if err != nil {
		fatal("Request failed", "url", url, "err", err)
	}
	defer res.Body.Close()

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		fatal("Failed to parse HTML", "url", url, "err", err)
	}
	return doc
}

// ---------- main ----------

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