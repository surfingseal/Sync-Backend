package config

import "testing"

func TestOptionalMobileEnvironment(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("MOBILE_APP_LINK_BASE_URL", "https://sync.example")
	t.Setenv("ANDROID_PACKAGE_NAME", "org.test.sync")
	t.Setenv("ANDROID_APP_SIGNING_SHA256", "not-yet-configured")
	cfg, err := Load()
	if err != nil {
		t.Fatal("mobile configuration must not kill existing server", err)
	}
	if cfg.MobileAppLinkBaseURL != "https://sync.example" || cfg.AndroidPackageName != "org.test.sync" || cfg.AndroidAppSigningSHA256 != "not-yet-configured" {
		t.Fatal("environment not loaded")
	}
}
