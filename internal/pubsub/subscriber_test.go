package pubsub

import (
	"os"
	"testing"
)

func TestPubsubEndpointFallback(t *testing.T) {
	// 1. Without env var set, should return default endpoint
	orig := os.Getenv("SF_PUBSUB_ENDPOINT")
	defer os.Setenv("SF_PUBSUB_ENDPOINT", orig)

	os.Unsetenv("SF_PUBSUB_ENDPOINT")
	if got := pubsubEndpoint(); got != "api.pubsub.salesforce.com:7443" {
		t.Errorf("pubsubEndpoint() default: got %q, want api.pubsub.salesforce.com:7443", got)
	}

	// 2. With env var set, should override
	custom := "sandbox.pubsub.salesforce.com:7443"
	os.Setenv("SF_PUBSUB_ENDPOINT", custom)
	if got := pubsubEndpoint(); got != custom {
		t.Errorf("pubsubEndpoint() override: got %q, want %q", got, custom)
	}
}

func TestPubsubTopicFallback(t *testing.T) {
	// 1. Without env var set, should return default topic
	orig := os.Getenv("SF_PUBSUB_TOPIC")
	defer os.Setenv("SF_PUBSUB_TOPIC", orig)

	os.Unsetenv("SF_PUBSUB_TOPIC")
	if got := pubsubTopic(); got != "/data/ChangeEvents" {
		t.Errorf("pubsubTopic() default: got %q, want /data/ChangeEvents", got)
	}

	// 2. With env var set, should override
	custom := "/data/CustomChangeEvents__e"
	os.Setenv("SF_PUBSUB_TOPIC", custom)
	if got := pubsubTopic(); got != custom {
		t.Errorf("pubsubTopic() override: got %q, want %q", got, custom)
	}
}
