package resourceconformance

import "encoding/json"

// NativeCorpus returns fresh, deterministic, credential-free data on each call.
// IDs and cursors deliberately contain delimiter, slash, percent and Unicode
// characters. Native numeric JSON remains unparsed; no float64 conversion.
func NativeCorpus() Corpus {
	cloudSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["id","account","region","tags"],"properties":{"id":{"type":"string"},"account":{"type":"string"},"region":{"type":"string"},"tags":{"type":"array","items":{"type":"object","required":["key","value"],"properties":{"key":{"type":"string"},"value":{"type":"string"}}}},"sizeBytes":{"type":"integer"},"description":{"type":["string","null"]},"createdAt":{"type":"string","format":"date-time"}}}`,
	)
	containerSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["id","engine","labels","state"],"properties":{"id":{"type":"string"},"engine":{"type":"string"},"labels":{"type":"object","additionalProperties":{"type":"string"}},"state":{"type":"string"},"generation":{"type":"string"},"revision":{"type":"string"}}}`,
	)
	vmSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["vmid","node","host","disks"],"properties":{"vmid":{"type":"integer"},"node":{"type":"string"},"host":{"type":"string"},"disks":{"type":"array","items":{"type":"integer"}},"name":{"type":"string"}}}`,
	)
	c := Corpus{Records: []Record{
		{
			Name:      "cloud-arn",
			Provider:  "cloud",
			Authority: "cloud-partition:fixture",
			Type:      "compute/instance",
			Scope:     map[string]string{"account": "111111111111", "region": "us-west-2"},
			ID:        "arn:fixture:compute:us-west-2:111111111111:instance/a::b/雪%2F",
			Revision:  "etag:Z/9",
			Body: json.RawMessage(
				`{"id":"arn:fixture:compute:us-west-2:111111111111:instance/a::b/雪%2F","account":"111111111111","region":"us-west-2","tags":[{"key":"env","value":"prod"},{"key":"team","value":"infra"}],"sizeBytes":9007199254740993,"description":null,"createdAt":"2026-10-17T12:00:00.123456789Z"}`,
			),
		},
		{
			Name:      "cloud-ocid",
			Provider:  "cloud",
			Authority: "cloud-partition:fixture",
			Type:      "compute/instance",
			Scope:     map[string]string{"account": "111111111111", "region": "us-west-2"},
			ID:        "ocid1.instance.fixture.us-west-2.a:b/c%25",
			Body: json.RawMessage(
				`{"id":"ocid1.instance.fixture.us-west-2.a:b/c%25","account":"111111111111","region":"us-west-2","tags":[{"key":"env","value":"dev"},{"key":"team","value":"prod"}],"sizeBytes":18446744073709551615,"createdAt":"2026-10-17T12:00:01Z"}`,
			),
		},
		{
			Name:      "cloud-other-region",
			Provider:  "cloud",
			Authority: "cloud-partition:fixture",
			Type:      "compute/instance",
			Scope:     map[string]string{"account": "111111111111", "region": "eu-central-1"},
			ID:        "opaque::same/id",
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"111111111111","region":"eu-central-1","tags":[]}`,
			),
		},
		{
			Name:      "cloud-other-account",
			Provider:  "cloud",
			Authority: "cloud-partition:fixture",
			Type:      "compute/instance",
			Scope:     map[string]string{"account": "222222222222", "region": "eu-central-1"},
			ID:        "opaque::same/id",
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"222222222222","region":"eu-central-1","tags":[{"key":"quoted.key/雪","value":"x' || true || '"}]}`,
			),
		},
		{
			Name:        "container-a",
			Provider:    "container",
			Authority:   "engine-fleet:fixture",
			Type:        "container",
			Scope:       map[string]string{"engine": "unix:///engine/a::socket"},
			ID:          "sha256:a::b/c%2F",
			Incarnation: "created:2026-10-17/a",
			Revision:    "event:z9",
			Body: json.RawMessage(
				`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{"com.example.team":"infra","quoted.key/雪":"x' || true || '"},"state":"running","generation":"created:2026-10-17/a","revision":"event:z9"}`,
			),
		},
		{
			Name:      "container-b",
			Provider:  "container",
			Authority: "engine-fleet:fixture",
			Type:      "container",
			Scope:     map[string]string{"engine": "unix:///engine/b::socket"},
			ID:        "sha256:a::b/c%2F",
			Body: json.RawMessage(
				`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/b::socket","labels":{},"state":"exited"}`,
			),
		},
		{
			Name:      "host-a",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "host",
			Scope:     map[string]string{"node": "node/a::1"},
			ID:        "node/a::1",
			Body:      json.RawMessage(`{"node":"node/a::1","name":"Host A"}`),
		},
		{
			Name:      "host-b",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "host",
			Scope:     map[string]string{"node": "node/b::1"},
			ID:        "node/b::1",
			Body:      json.RawMessage(`{"node":"node/b::1","name":"Host B"}`),
		},
		{
			Name:      "vm-a",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "vm",
			Scope:     map[string]string{"node": "node/a::1"},
			ID:        "101",
			Body:      json.RawMessage(`{"vmid":101,"node":"node/a::1","host":"node/a::1","disks":[7],"name":"db"}`),
		},
		{
			Name:      "vm-b",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "vm",
			Scope:     map[string]string{"node": "node/b::1"},
			ID:        "101",
			Revision:  "snapshot:opaque/A",
			Body: json.RawMessage(
				`{"vmid":101,"node":"node/b::1","host":"node/b::1","disks":[7],"name":"worker"}`,
			),
		},
		{
			Name:      "disk-a",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "disk",
			Scope:     map[string]string{"node": "node/a::1"},
			ID:        "7",
			Body:      json.RawMessage(`{"diskid":7,"node":"node/a::1","vmid":101,"sizeBytes":9007199254740993}`),
		},
		{
			Name:      "disk-b",
			Provider:  "hypervisor",
			Authority: "cluster:fixture",
			Type:      "disk",
			Scope:     map[string]string{"node": "node/b::1"},
			ID:        "7",
			Body:      json.RawMessage(`{"diskid":7,"node":"node/b::1","vmid":101,"sizeBytes":4294967296}`),
		},
		{
			Name:      "cloud-west-local",
			Provider:  "cloud",
			Authority: "cloud-partition:fixture",
			Type:      "compute/instance",
			Scope:     map[string]string{"account": "111111111111", "region": "us-west-2"},
			ID:        "opaque::same/id",
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"111111111111","region":"us-west-2","tags":[]}`,
			),
		},
	}}
	// A second access route and a recreation observe the same address. These are
	// oracle records only, not additional members of any enumeration.
	alias := c.Record("cloud-arn")
	alias.Name = "cloud-second-route"
	alias.Scope = map[string]string{"region": "us-west-2", "account": "111111111111"}
	c.Records = append(c.Records, alias)
	recreated := c.Record("container-a")
	recreated.Name, recreated.Incarnation, recreated.Revision = "container-recreated", "created:2026-10-18/b", "event:a1"
	recreated.Body = json.RawMessage(
		`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{},"state":"running","generation":"created:2026-10-18/b","revision":"event:a1"}`,
	)
	c.Records = append(c.Records, recreated)
	cloudSelection := func() map[string][]string {
		return map[string][]string{"account": {"111111111111"}, "region": {"us-west-2"}}
	}
	engineSelection := func() map[string][]string {
		return map[string][]string{"engine": {"unix:///engine/a::socket", "unix:///engine/b::socket"}}
	}
	nodeSelection := func() map[string][]string { return map[string][]string{"node": {"node/a::1", "node/b::1"}} }
	bestEffort := func() Coverage {
		return Coverage{
			Enumeration: "exhausted",
			Consistency: "bestEffort",
			Limitations: []string{"exhaustion is relative to selected access; no atomic multi-page snapshot"},
		}
	}
	c.Scenarios = []Scenario{
		{
			Name:      "cloud-bundled-pages",
			Profile:   "cloud",
			Type:      "compute/instance",
			Selection: cloudSelection(),
			Schema:    cloudSchema,
			Pages: []Page{
				{Next: "native:page/2::雪%2F", Records: []string{"cloud-arn"}},
				{Cursor: "native:page/2::雪%2F", Records: []string{"cloud-ocid"}},
			},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "cloud-absent-schema",
			Profile:   "cloud",
			Type:      "compute/instance",
			Selection: cloudSelection(),
			Pages:     []Page{{Records: []string{"cloud-arn", "cloud-ocid"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "cloud-partial-eventual",
			Profile:   "cloud",
			Type:      "compute/instance",
			Selection: cloudSelection(),
			Pages:     []Page{{Records: []string{"cloud-arn"}}},
			Coverage: Coverage{
				Enumeration: "partial",
				Consistency: "bestEffort",
				Limitations: []string{"selected listing truncated by source; eventual visibility; unknown total"},
			},
			Freshness: "eventual-snapshot",
		},
		{
			Name:      "cloud-account-region-collisions",
			Profile:   "cloud",
			Type:      "compute/instance",
			Selection: map[string][]string{"account": {"111111111111", "222222222222"}, "region": {"eu-central-1"}},
			Pages:     []Page{{Records: []string{"cloud-other-region", "cloud-other-account"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "container-bundled-events",
			Profile:   "container",
			Type:      "container",
			Selection: engineSelection(),
			Schema:    containerSchema,
			Pages:     []Page{{Records: []string{"container-a", "container-b"}}},
			Coverage: Coverage{
				Enumeration: "exhausted",
				Consistency: "bestEffort",
				Limitations: []string{"native event feed has no replay or gap-free snapshot handoff"},
			},
			Freshness: "best-effort-updates",
			Updates: []Change{
				{
					Record:   "container-a",
					Revision: "event:a1",
					Body: json.RawMessage(
						`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{"com.example.team":"infra","quoted.key/雪":"x' || true || '"},"state":"paused","generation":"created:2026-10-17/a","revision":"event:a1"}`,
					),
				},
			},
		},
		{
			Name:      "container-absent-snapshot",
			Profile:   "container",
			Type:      "container",
			Selection: engineSelection(),
			Pages:     []Page{{Records: []string{"container-a", "container-b"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-bundled-vms",
			Profile:   "hypervisor",
			Type:      "vm",
			Selection: nodeSelection(),
			Schema:    vmSchema,
			Pages: []Page{
				{Next: "node-page::2/", Records: []string{"vm-a"}},
				{Cursor: "node-page::2/", Records: []string{"vm-b"}},
			},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-absent-vms",
			Profile:   "hypervisor",
			Type:      "vm",
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{"vm-a", "vm-b"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-hosts",
			Profile:   "hypervisor",
			Type:      "host",
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{"host-a", "host-b"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-disks",
			Profile:   "hypervisor",
			Type:      "disk",
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{"disk-a", "disk-b"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "cloud-region-collisions",
			Profile:   "cloud",
			Type:      "compute/instance",
			Selection: map[string][]string{"account": {"111111111111"}, "region": {"us-west-2", "eu-central-1"}},
			Pages:     []Page{{Records: []string{"cloud-west-local", "cloud-other-region"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-node-a-only",
			Profile:   "hypervisor",
			Type:      "vm",
			Selection: map[string][]string{"node": {"node/a::1"}},
			Pages:     []Page{{Records: []string{"vm-a"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
		{
			Name:      "hypervisor-node-b-only",
			Profile:   "hypervisor",
			Type:      "vm",
			Selection: map[string][]string{"node": {"node/b::1"}},
			Pages:     []Page{{Records: []string{"vm-b"}}},
			Coverage:  bestEffort(),
			Freshness: "snapshot-only",
		},
	}
	c.IdentityCases = []IdentityCase{
		{Name: "opaque-address-stable-across-access", Left: "cloud-arn", Right: "cloud-second-route", Equal: true},
		{Name: "account-is-identity-scope", Left: "cloud-other-region", Right: "cloud-other-account", Equal: false},
		{Name: "region-is-identity-scope", Left: "cloud-west-local", Right: "cloud-other-region", Equal: false},
		{Name: "engine-local-ID-collision", Left: "container-a", Right: "container-b", Equal: false},
		{Name: "node-local-VM-ID-collision", Left: "vm-a", Right: "vm-b", Equal: false},
		{Name: "node-local-disk-ID-collision", Left: "disk-a", Right: "disk-b", Equal: false},
		{Name: "incarnation-not-address", Left: "container-a", Right: "container-recreated", Equal: true},
	}
	c.Links = []Link{
		{"vm-a", "hosted-by", "host-a"},
		{"vm-b", "hosted-by", "host-b"},
		{"vm-a", "has-disk", "disk-a"},
		{"vm-b", "has-disk", "disk-b"},
		{"disk-a", "attached-to", "vm-a"},
		{"disk-b", "attached-to", "vm-b"},
	}
	for i := range c.Scenarios {
		s := &c.Scenarios[i]
		s.Operations = map[string]string{
			"enumerate":         "supported",
			"get-exact-address": "supported",
			"server-filter":     "unsupported",
			"create":            "unsupported",
			"update":            "unsupported",
			"delete":            "unsupported",
			"updates":           "unsupported",
		}
		if len(s.Updates) > 0 {
			s.Operations["updates"] = "supported-best-effort"
		}
	}
	return c
}
