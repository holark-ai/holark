package ideworkbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
)

const (
	MaxHTMLBytes                 = 1 << 20
	workbenchConfigurationMarker = `id="vscode-workbench-web-configuration"`
)

func IsHTMLContentType(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "text/html")
}

func Validate(document []byte) error {
	_, err := Rewrite(document, "dark")
	return err
}

func Rewrite(document []byte, theme string) ([]byte, error) {
	elementID := bytes.Index(document, []byte(workbenchConfigurationMarker))
	if elementID < 0 {
		return nil, errors.New("VS Code workbench configuration is missing")
	}
	tagStart := bytes.LastIndex(document[:elementID], []byte("<meta"))
	tagEndOffset := bytes.IndexByte(document[elementID:], '>')
	if tagStart < 0 || tagEndOffset < 0 {
		return nil, errors.New("VS Code workbench configuration element is invalid")
	}
	tagEnd := elementID + tagEndOffset
	tag := document[tagStart:tagEnd]
	attribute := []byte(`data-settings="`)
	attributeOffset := bytes.Index(tag, attribute)
	if attributeOffset < 0 {
		return nil, errors.New("VS Code workbench settings are missing")
	}
	settingsStart := tagStart + attributeOffset + len(attribute)
	settingsEndOffset := bytes.IndexByte(document[settingsStart:tagEnd], '"')
	if settingsEndOffset < 0 {
		return nil, errors.New("VS Code workbench settings are invalid")
	}
	settingsEnd := settingsStart + settingsEndOffset

	var options map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(document[settingsStart:settingsEnd]))), &options); err != nil {
		return nil, fmt.Errorf("decode VS Code workbench settings: %w", err)
	}
	if options == nil {
		return nil, errors.New("VS Code workbench settings are invalid")
	}
	options["enableWorkspaceTrust"] = false
	configurationDefaults, ok := options["configurationDefaults"].(map[string]any)
	if !ok {
		if options["configurationDefaults"] != nil {
			return nil, errors.New("VS Code workbench configuration defaults are invalid")
		}
		configurationDefaults = make(map[string]any)
		options["configurationDefaults"] = configurationDefaults
	}
	settingsID := "Default Dark Modern"
	if theme == "light" {
		settingsID = "Default Light Modern"
	}
	configurationDefaults["workbench.colorTheme"] = settingsID
	options["initialColorTheme"] = map[string]any{"themeType": theme}

	encoded, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("encode VS Code workbench settings: %w", err)
	}
	escaped := []byte(html.EscapeString(string(encoded)))
	rewritten := make([]byte, 0, len(document)-settingsEnd+settingsStart+len(escaped))
	rewritten = append(rewritten, document[:settingsStart]...)
	rewritten = append(rewritten, escaped...)
	rewritten = append(rewritten, document[settingsEnd:]...)
	return rewritten, nil
}
