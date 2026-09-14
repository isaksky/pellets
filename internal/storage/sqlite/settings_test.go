package sqlite

import (
	"context"
	"sync"
	"testing"
)

func TestSettingsPersistenceAndIndependentWrites(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	if err := writer.SaveSetting(ctx, "theme", "icy", false); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveSetting(ctx, "theme", "dark", true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for key, value := range map[string]string{"navigation_visible": "false", "execution_visible": "true", "right_panel_tab": "plan"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writer.SaveSetting(ctx, key, value, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	reopened, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.ReadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "icy" || settings["navigation_visible"] != "false" || settings["right_panel_tab"] != "plan" || len(settings) != 4 {
		t.Fatalf("settings: %#v", settings)
	}
	for key, value := range map[string]string{"theme": "unknown", "credentials": "secret", "execution_visible": "yes"} {
		if err := writer.SaveSetting(ctx, key, value, false); err == nil {
			t.Fatalf("accepted %s=%s", key, value)
		}
	}
	values, err := reader.ReadSettings(ctx)
	if err != nil || len(values) != 4 {
		t.Fatalf("invalid write changed settings: %#v %v", values, err)
	}
}
