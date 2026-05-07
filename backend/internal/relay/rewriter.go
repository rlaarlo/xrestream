package relay

import (
	"bufio"
	"bytes"
	"net/url"
	"regexp"
	"strings"
)

var uriAttributePattern = regexp.MustCompile(`URI="([^"]+)"`)

type AssetRegistry interface {
	RegisterAsset(slug, sourceURL string) (ref string, signedURL string)
}

type RewriteResult struct {
	Playlist string
	Assets   []string
}

func RewritePlaylist(source string, sourcePlaylistURL string, slug string, registry AssetRegistry) (RewriteResult, error) {
	baseURL, err := url.Parse(sourcePlaylistURL)
	if err != nil {
		return RewriteResult{}, err
	}

	assets := []string{}
	var output bytes.Buffer
	scanner := bufio.NewScanner(strings.NewReader(source))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			output.WriteByte('\n')
			continue
		}

		if strings.HasPrefix(line, "#") {
			rewritten := uriAttributePattern.ReplaceAllStringFunc(line, func(match string) string {
				parts := uriAttributePattern.FindStringSubmatch(match)
				if len(parts) != 2 {
					return match
				}
				resolved := resolveURL(baseURL, parts[1])
				assets = append(assets, resolved)
				_, signedURL := registry.RegisterAsset(slug, resolved)
				return `URI="` + signedURL + `"`
			})
			output.WriteString(rewritten)
			output.WriteByte('\n')
			continue
		}

		resolved := resolveURL(baseURL, line)
		assets = append(assets, resolved)
		_, signedURL := registry.RegisterAsset(slug, resolved)
		output.WriteString(signedURL)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return RewriteResult{}, err
	}
	return RewriteResult{Playlist: output.String(), Assets: assets}, nil
}

func resolveURL(base *url.URL, raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(parsed).String()
}
