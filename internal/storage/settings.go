package storage

import (
	"context"
	"pellets/internal/domain"
)

// Settings are database-wide presentation preferences, separate from project
// execution settings and queue selection. Each key is written independently.
type Settings map[string]string

type SettingsReader interface {
	ReadSettings(context.Context) (Settings, error)
}
type SettingsWriter interface {
	SaveSetting(context.Context, string, string, bool) error
}

func ValidateSetting(key, value string) error {
	valid := false
	switch key {
	case "theme":
		switch value {
		case "gruvbox-light", "gruvbox-dark", "light", "dark", "icy":
			valid = true
		}
	case "navigation_visible", "execution_visible":
		valid = value == "true" || value == "false"
	case "right_panel_tab":
		valid = value == "plan" || value == "execution"
	}
	if !valid {
		return domain.NewError(domain.Usage, "invalid_setting", "Choose a supported presentation setting and value.", nil)
	}
	return nil
}
