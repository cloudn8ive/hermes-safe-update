package config

// Defaults returns a fresh copy of the built-in configuration: the numbers
// and the console look of the Python updater this tool replaces.
func Defaults() File {
	return File{
		Tunables: Tunables{
			MinFreeGB:       5,
			IdleWindowS:     180,
			IdlePollS:       60,
			IdleWaitMaxS:    1800,
			ConfirmTimeoutS: 30,
			GracefulCloseS:  30,
			UpdateTimeoutS:  5400,
			SilentWarnS:     600,
			VerifyWaitS:     120,
			MarkerRefreshS:  120,

			EstimateWindow:    5,
			EstimateOverrunK:  0.5,
			EstimateClassDamp: 0.15,
			EstimateClassMin:  0.8,
			EstimateClassMax:  2.5,
			HistoryKeep:       40,

			StepSeconds: map[string]int{
				// update run
				"local": 1, "remote": 75, "procs": 3,
				"wait": 0, "close": 8,
				"gateway_stop": 23, "fetch": 10, "pull": 90, "pydeps": 64,
				"frontend": 25, "desktop": 210, "package": 22,
				"finalize": 12, "restart": 30, "verify": 5, "settings": 10,
				"relaunch": 1, "cleanup": 3, "cua": 7, "hooks": 8,
				// check run
				"sessions": 1, "gc_preview": 9, "cua_status": 5,
			},
		},
		Theme: Theme{
			Margin:       2,
			LabelWidth:   15,
			StatusWidth:  50,
			MaxWidth:     400,
			BarBand:      6,
			BarFrom:      [3]int{95, 175, 255},
			BarTo:        [3]int{95, 215, 135},
			DateTopRight: true,
			Taskbar:      true,
			Flash:        true,
			TitleUpdate:  "Hermes Safe Update",
			TitleCheck:   "Hermes Update Check",
			Colors: Colors{
				Accent: "38;5;80", OK: "38;5;78", Warn: "38;5;221", Err: "38;5;203",
				Dim: "38;5;245", Faint: "38;5;240", Soft: "38;5;250", Label: "38;5;110",
				Token: "38;5;183", Neutral: "38;5;242", Track: "38;5;236", Prompt: "38;5;221;1",
			},
			Glyphs: Glyphs{
				OK: "√", Err: "×", Warn: "!", Info: "i", Command: "»", Active: "►",
				Pending: "○", Dot: "●", Bar: "█", Rule: "━", RuleThin: "─", Sep: "·",
				Gutter: "│", Arrow: "→", Title: "♦", Section: "▸",
			},
			Hues: map[string]Hue{
				"Check":        {Bright: 80, Dark: 30, Icon: "◊"},
				"Close Hermes": {Bright: 215, Dark: 130, Icon: "■"},
				"Update":       {Bright: 75, Dark: 25, Icon: "↓"},
				"Build":        {Bright: 141, Dark: 97, Icon: "≡"},
				"Finish":       {Bright: 78, Dark: 29, Icon: "♦"},
				"Local":        {Bright: 80, Dark: 30, Icon: "⌂"},
				"Updates":      {Bright: 75, Dark: 25, Icon: "↓"},
				"Hermes":       {Bright: 141, Dark: 97, Icon: "♦"},
				"Housekeeping": {Bright: 78, Dark: 29, Icon: "♣"},
			},
			SectionIcons: map[string]string{
				"Dependency generation cleanup": "♣",
				"Verify":                        "√",
				"cua-driver":                    "►",
				"Close":                         "■",
				"Relaunch":                      "↑",
				"Pre-flight":                    "◊",
				"Settings":                      "≡",
			},
		},
		Mirror: Mirror{
			Offer:         true,
			OfferTimeoutS: 45,
			CronSchedule:  "every 4h",
			JobName:       "Hermes update mirror refresh",
			KeepFreeGB:    5,
			Growth:        0.5,
		},
		Settings: Settings{
			AutoMigrate:     true,
			BackupRetention: 3,
			KeepNew: []string{
				"hermes.desktop.lastRoute.profile.*",
				"hermes.desktop.lastSessionId.profile.*",
				"hermes.desktop.freshDraftKey",
				"hermes.updates.last-passive-check",
				"hermes.desktop.tips.next.v1",
			},
			Union: []string{
				"hermes.desktop.threadScroll.v1.profile.*",
				"hermes.desktop.sessionSeenCounts",
				"hermes.desktop.unreadFinishedSessions",
				"hermes.desktop.sessionOwnerHints.v1",
				"hermes.desktop.toolDisclosure.v1",
			},
		},
		Logging: Logging{Level: "info", MaxSizeMB: 5, Keep: 3},
	}
}
