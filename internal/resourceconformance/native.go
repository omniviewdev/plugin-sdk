package resourceconformance

import (
	"encoding/json"
	"slices"
)

const (
	recordCloudARN          = "cloud-arn"
	recordCloudOCID         = "cloud-ocid"
	recordCloudOtherRegion  = "cloud-other-region"
	recordCloudOtherAccount = "cloud-other-account"
	recordCloudWest         = "cloud-west-local"
	recordContainerA        = "container-a"
	recordContainerB        = "container-b"
	recordHostA             = "host-a"
	recordHostB             = "host-b"
	recordVMA               = "vm-a"
	recordVMB               = "vm-b"
	recordDiskA             = "disk-a"
	recordDiskB             = "disk-b"

	providerCloud       = "cloud"
	providerContainer   = "container"
	providerHypervisor  = "hypervisor"
	authorityCloud      = "cloud-partition:fixture"
	authorityHypervisor = "cluster:fixture"
	typeComputeInstance = "compute/instance"
	typeHost            = "host"
	typeDisk            = "disk"
	scopeAccount        = "account"
	scopeRegion         = "region"
	scopeEngine         = "engine"
	scopeNode           = "node"
	regionWest          = "us-west-2"
	regionCentral       = "eu-central-1"
	nodeA               = "node/a::1"
	nodeB               = "node/b::1"
	collidingCloudID    = "opaque::same/id"

	consistencyBestEffort = "bestEffort"
	freshnessSnapshot     = "snapshot-only"
	operationUnsupported  = "unsupported"
	relationHostedBy      = "hosted-by"
	relationHasDisk       = "has-disk"
	relationAttachedTo    = "attached-to"
)

// NativeCorpus returns fresh, deterministic, credential-free data on each call.
// IDs and cursors deliberately contain delimiter, slash, percent and Unicode
// characters. Native numeric JSON remains unparsed; no float64 conversion.
func NativeCorpus() Corpus {
	c := Corpus{Records: slices.Concat(nativeCloudRecords(), nativeContainerRecords(), nativeHypervisorRecords())}
	c.addIdentityOracleRecords()
	c.Scenarios = slices.Concat(
		nativeCloudScenarios(),
		nativeContainerScenarios(),
		nativeHypervisorScenarios(),
		nativeScopeCollisionScenarios(),
	)
	c.IdentityCases = []IdentityCase{
		{Name: "opaque-address-stable-across-access", Left: recordCloudARN, Right: "cloud-second-route", Equal: true},
		{Name: "account-is-identity-scope", Left: recordCloudOtherRegion, Right: recordCloudOtherAccount, Equal: false},
		{Name: "region-is-identity-scope", Left: recordCloudWest, Right: recordCloudOtherRegion, Equal: false},
		{Name: "engine-local-ID-collision", Left: recordContainerA, Right: recordContainerB, Equal: false},
		{Name: "node-local-VM-ID-collision", Left: recordVMA, Right: recordVMB, Equal: false},
		{Name: "node-local-disk-ID-collision", Left: recordDiskA, Right: recordDiskB, Equal: false},
		{Name: "incarnation-not-address", Left: recordContainerA, Right: "container-recreated", Equal: true},
	}
	c.Links = []Link{
		{recordVMA, relationHostedBy, recordHostA},
		{recordVMB, relationHostedBy, recordHostB},
		{recordVMA, relationHasDisk, recordDiskA},
		{recordVMB, relationHasDisk, recordDiskB},
		{recordDiskA, relationAttachedTo, recordVMA},
		{recordDiskB, relationAttachedTo, recordVMB},
	}
	for i := range c.Scenarios {
		s := &c.Scenarios[i]
		s.Operations = map[string]string{
			"enumerate":         "supported",
			"get-exact-address": "supported",
			"server-filter":     operationUnsupported,
			"create":            operationUnsupported,
			"update":            operationUnsupported,
			"delete":            operationUnsupported,
			"updates":           operationUnsupported,
		}
		if len(s.Updates) > 0 {
			s.Operations["updates"] = "supported-best-effort"
		}
	}
	return c
}

func nativeCloudRecords() []Record {
	return []Record{
		{
			Name:      recordCloudARN,
			Provider:  providerCloud,
			Authority: authorityCloud,
			Type:      typeComputeInstance,
			Scope:     map[string]string{scopeAccount: "111111111111", scopeRegion: regionWest},
			ID:        "arn:fixture:compute:us-west-2:111111111111:instance/a::b/雪%2F",
			Revision:  "etag:Z/9",
			Body: json.RawMessage(
				`{"id":"arn:fixture:compute:us-west-2:111111111111:instance/a::b/雪%2F","account":"111111111111","region":"us-west-2","tags":[{"key":"env","value":"prod"},{"key":"team","value":"infra"}],"sizeBytes":9007199254740993,"description":null,"createdAt":"2026-10-17T12:00:00.123456789Z"}`,
			),
		},
		{
			Name:      recordCloudOCID,
			Provider:  providerCloud,
			Authority: authorityCloud,
			Type:      typeComputeInstance,
			Scope:     map[string]string{scopeAccount: "111111111111", scopeRegion: regionWest},
			ID:        "ocid1.instance.fixture.us-west-2.a:b/c%25",
			Body: json.RawMessage(
				`{"id":"ocid1.instance.fixture.us-west-2.a:b/c%25","account":"111111111111","region":"us-west-2","tags":[{"key":"env","value":"dev"},{"key":"team","value":"prod"}],"sizeBytes":18446744073709551615,"createdAt":"2026-10-17T12:00:01Z"}`,
			),
		},
		{
			Name:      recordCloudOtherRegion,
			Provider:  providerCloud,
			Authority: authorityCloud,
			Type:      typeComputeInstance,
			Scope:     map[string]string{scopeAccount: "111111111111", scopeRegion: regionCentral},
			ID:        collidingCloudID,
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"111111111111","region":"eu-central-1","tags":[]}`,
			),
		},
		{
			Name:      recordCloudOtherAccount,
			Provider:  providerCloud,
			Authority: authorityCloud,
			Type:      typeComputeInstance,
			Scope:     map[string]string{scopeAccount: "222222222222", scopeRegion: regionCentral},
			ID:        collidingCloudID,
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"222222222222","region":"eu-central-1","tags":[{"key":"quoted.key/雪","value":"x' || true || '"}]}`,
			),
		},
	}
}

func nativeContainerRecords() []Record {
	return []Record{
		{
			Name:        recordContainerA,
			Provider:    providerContainer,
			Authority:   "engine-fleet:fixture",
			Type:        providerContainer,
			Scope:       map[string]string{scopeEngine: "unix:///engine/a::socket"},
			ID:          "sha256:a::b/c%2F",
			Incarnation: "created:2026-10-17/a",
			Revision:    "event:z9",
			Body: json.RawMessage(
				`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{"com.example.team":"infra","quoted.key/雪":"x' || true || '"},"state":"running","generation":"created:2026-10-17/a","revision":"event:z9"}`,
			),
		},
		{
			Name:      recordContainerB,
			Provider:  providerContainer,
			Authority: "engine-fleet:fixture",
			Type:      providerContainer,
			Scope:     map[string]string{scopeEngine: "unix:///engine/b::socket"},
			ID:        "sha256:a::b/c%2F",
			Body: json.RawMessage(
				`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/b::socket","labels":{},"state":"exited"}`,
			),
		},
	}
}

func nativeHypervisorRecords() []Record {
	return []Record{
		{
			Name:      recordHostA,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      typeHost,
			Scope:     map[string]string{scopeNode: nodeA},
			ID:        nodeA,
			Body:      json.RawMessage(`{"node":"node/a::1","name":"Host A"}`),
		},
		{
			Name:      recordHostB,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      typeHost,
			Scope:     map[string]string{scopeNode: nodeB},
			ID:        nodeB,
			Body:      json.RawMessage(`{"node":"node/b::1","name":"Host B"}`),
		},
		{
			Name:      recordVMA,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      "vm",
			Scope:     map[string]string{scopeNode: nodeA},
			ID:        "101",
			Body:      json.RawMessage(`{"vmid":101,"node":"node/a::1","host":"node/a::1","disks":[7],"name":"db"}`),
		},
		{
			Name:      recordVMB,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      "vm",
			Scope:     map[string]string{scopeNode: nodeB},
			ID:        "101",
			Revision:  "snapshot:opaque/A",
			Body: json.RawMessage(
				`{"vmid":101,"node":"node/b::1","host":"node/b::1","disks":[7],"name":"worker"}`,
			),
		},
		{
			Name:      recordDiskA,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      typeDisk,
			Scope:     map[string]string{scopeNode: nodeA},
			ID:        "7",
			Body:      json.RawMessage(`{"diskid":7,"node":"node/a::1","vmid":101,"sizeBytes":9007199254740993}`),
		},
		{
			Name:      recordDiskB,
			Provider:  providerHypervisor,
			Authority: authorityHypervisor,
			Type:      typeDisk,
			Scope:     map[string]string{scopeNode: nodeB},
			ID:        "7",
			Body:      json.RawMessage(`{"diskid":7,"node":"node/b::1","vmid":101,"sizeBytes":4294967296}`),
		},
	}
}

func (c *Corpus) addIdentityOracleRecords() {
	c.Records = append(c.Records, []Record{
		{
			Name:      recordCloudWest,
			Provider:  providerCloud,
			Authority: authorityCloud,
			Type:      typeComputeInstance,
			Scope:     map[string]string{scopeAccount: "111111111111", scopeRegion: regionWest},
			ID:        collidingCloudID,
			Body: json.RawMessage(
				`{"id":"opaque::same/id","account":"111111111111","region":"us-west-2","tags":[]}`,
			),
		},
	}...)
	// A second access route and a recreation observe the same address. These are
	// oracle records only, not additional members of any enumeration.
	alias := c.Record(recordCloudARN)
	alias.Name = "cloud-second-route"
	alias.Scope = map[string]string{scopeRegion: regionWest, scopeAccount: "111111111111"}
	c.Records = append(c.Records, alias)
	recreated := c.Record(recordContainerA)
	recreated.Name, recreated.Incarnation, recreated.Revision = "container-recreated", "created:2026-10-18/b", "event:a1"
	recreated.Body = json.RawMessage(
		`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{},"state":"running","generation":"created:2026-10-18/b","revision":"event:a1"}`,
	)
	c.Records = append(c.Records, recreated)
}

func nativeBestEffortCoverage() Coverage {
		return Coverage{
			Enumeration: "exhausted",
			Consistency: consistencyBestEffort,
			Limitations: []string{"exhaustion is relative to selected access; no atomic multi-page snapshot"},
		}
}

func nativeCloudScenarios() []Scenario {
	cloudSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["id","account","region","tags"],"properties":{"id":{"type":"string"},"account":{"type":"string"},"region":{"type":"string"},"tags":{"type":"array","items":{"type":"object","required":["key","value"],"properties":{"key":{"type":"string"},"value":{"type":"string"}}}},"sizeBytes":{"type":"integer"},"description":{"type":["string","null"]},"createdAt":{"type":"string","format":"date-time"}}}`,
	)
	cloudSelection := func() map[string][]string {
		return map[string][]string{scopeAccount: {"111111111111"}, scopeRegion: {regionWest}}
	}
	return []Scenario{
		{
			Name:      "cloud-bundled-pages",
			Profile:   providerCloud,
			Type:      typeComputeInstance,
			Selection: cloudSelection(),
			Schema:    cloudSchema,
			Pages: []Page{
				{Next: "native:page/2::雪%2F", Records: []string{recordCloudARN}},
				{Cursor: "native:page/2::雪%2F", Records: []string{recordCloudOCID}},
			},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "cloud-absent-schema",
			Profile:   providerCloud,
			Type:      typeComputeInstance,
			Selection: cloudSelection(),
			Pages:     []Page{{Records: []string{recordCloudARN, recordCloudOCID}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "cloud-partial-eventual",
			Profile:   providerCloud,
			Type:      typeComputeInstance,
			Selection: cloudSelection(),
			Pages:     []Page{{Records: []string{recordCloudARN}}},
			Coverage: Coverage{
				Enumeration: "partial",
				Consistency: consistencyBestEffort,
				Limitations: []string{"selected listing truncated by source; eventual visibility; unknown total"},
			},
			Freshness: "eventual-snapshot",
		},
		{
			Name:    "cloud-account-region-collisions",
			Profile: providerCloud,
			Type:    typeComputeInstance,
			Selection: map[string][]string{
				scopeAccount: {"111111111111", "222222222222"},
				scopeRegion:  {regionCentral},
			},
			Pages:     []Page{{Records: []string{recordCloudOtherRegion, recordCloudOtherAccount}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
	}
}

func nativeContainerScenarios() []Scenario {
	containerSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["id","engine","labels","state"],"properties":{"id":{"type":"string"},"engine":{"type":"string"},"labels":{"type":"object","additionalProperties":{"type":"string"}},"state":{"type":"string"},"generation":{"type":"string"},"revision":{"type":"string"}}}`,
	)
	engineSelection := func() map[string][]string {
		return map[string][]string{scopeEngine: {"unix:///engine/a::socket", "unix:///engine/b::socket"}}
	}
	return []Scenario{
		{
			Name:      "container-bundled-events",
			Profile:   providerContainer,
			Type:      providerContainer,
			Selection: engineSelection(),
			Schema:    containerSchema,
			Pages:     []Page{{Records: []string{recordContainerA, recordContainerB}}},
			Coverage: Coverage{
				Enumeration: "exhausted",
				Consistency: consistencyBestEffort,
				Limitations: []string{"native event feed has no replay or gap-free snapshot handoff"},
			},
			Freshness: "best-effort-updates",
			Updates: []Change{
				{
					Record:   recordContainerA,
					Revision: "event:a1",
					Body: json.RawMessage(
						`{"id":"sha256:a::b/c%2F","engine":"unix:///engine/a::socket","labels":{"com.example.team":"infra","quoted.key/雪":"x' || true || '"},"state":"paused","generation":"created:2026-10-17/a","revision":"event:a1"}`,
					),
				},
			},
		},
		{
			Name:      "container-absent-snapshot",
			Profile:   providerContainer,
			Type:      providerContainer,
			Selection: engineSelection(),
			Pages:     []Page{{Records: []string{recordContainerA, recordContainerB}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
	}
}

func nativeHypervisorScenarios() []Scenario {
	vmSchema := json.RawMessage(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["vmid","node","host","disks"],"properties":{"vmid":{"type":"integer"},"node":{"type":"string"},"host":{"type":"string"},"disks":{"type":"array","items":{"type":"integer"}},"name":{"type":"string"}}}`,
	)
	nodeSelection := func() map[string][]string { return map[string][]string{scopeNode: {nodeA, nodeB}} }
	return []Scenario{
		{
			Name:      "hypervisor-bundled-vms",
			Profile:   providerHypervisor,
			Type:      "vm",
			Selection: nodeSelection(),
			Schema:    vmSchema,
			Pages: []Page{
				{Next: "node-page::2/", Records: []string{recordVMA}},
				{Cursor: "node-page::2/", Records: []string{recordVMB}},
			},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "hypervisor-absent-vms",
			Profile:   providerHypervisor,
			Type:      "vm",
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{recordVMA, recordVMB}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "hypervisor-hosts",
			Profile:   providerHypervisor,
			Type:      typeHost,
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{recordHostA, recordHostB}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "hypervisor-disks",
			Profile:   providerHypervisor,
			Type:      typeDisk,
			Selection: nodeSelection(),
			Pages:     []Page{{Records: []string{recordDiskA, recordDiskB}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
	}
}

func nativeScopeCollisionScenarios() []Scenario {
	return []Scenario{
		{
			Name:      "cloud-region-collisions",
			Profile:   providerCloud,
			Type:      typeComputeInstance,
			Selection: map[string][]string{scopeAccount: {"111111111111"}, scopeRegion: {regionWest, regionCentral}},
			Pages:     []Page{{Records: []string{recordCloudWest, recordCloudOtherRegion}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "hypervisor-node-a-only",
			Profile:   providerHypervisor,
			Type:      "vm",
			Selection: map[string][]string{scopeNode: {nodeA}},
			Pages:     []Page{{Records: []string{recordVMA}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
		{
			Name:      "hypervisor-node-b-only",
			Profile:   providerHypervisor,
			Type:      "vm",
			Selection: map[string][]string{scopeNode: {nodeB}},
			Pages:     []Page{{Records: []string{recordVMB}}},
			Coverage:  nativeBestEffortCoverage(),
			Freshness: freshnessSnapshot,
		},
	}
}
