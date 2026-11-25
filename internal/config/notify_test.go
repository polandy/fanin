package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const signalNotify = `notify:
  kind: signal
  signal_number: "+15550100"
  ui_url: https://fanin.example/ui
  prefix: "[nuc/fanin]"
`

// TestNotify covers SCENARIOS OA-7: the push channel is the log unless the config names signal.
func TestNotify(t *testing.T) {
	t.Run("OA-7_default_is_the_log_adapter", func(t *testing.T) {
		cfg, _ := mustLoad(t, baseTree())
		if want := (Notify{Kind: NotifyLog, SignalCLI: "signal-cli"}); cmp.Diff(want, cfg.Notify) != "" {
			t.Errorf("Notify = %+v, want %+v", cfg.Notify, want)
		}
	})
	t.Run("OA-7_signal_keys_are_read", func(t *testing.T) {
		cfg, _ := mustLoad(t, with(map[string]string{fileFanin: faninYAML + signalNotify}))
		want := Notify{Kind: NotifySignal, SignalNumber: "+15550100", UIURL: "https://fanin.example/ui",
			Prefix: "[nuc/fanin]", SignalCLI: "signal-cli"}
		if diff := cmp.Diff(want, cfg.Notify); diff != "" {
			t.Errorf("Notify mismatch (-want +got):\n%s", diff)
		}
	})
	for name, yaml := range map[string]string{
		"signal_without_number": "notify:\n  kind: signal\n  ui_url: https://fanin.example/ui\n",
		"signal_without_ui_url": "notify:\n  kind: signal\n  signal_number: \"+15550100\"\n",
		"number_not_e164":       "notify:\n  kind: signal\n  signal_number: \"+15550100x\"\n  ui_url: https://fanin.example/ui\n",
		"unknown_key":           "notify:\n  kind: log\n  colour: red\n",
	} {
		t.Run("OA-7_refused_"+name, func(t *testing.T) {
			_, err := Load(writeTree(t, with(map[string]string{fileFanin: faninYAML + yaml})))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), "5550100") {
				t.Errorf("error repeats the number: %v", err)
			}
		})
	}
}
