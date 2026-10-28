package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func (c *Client) Features(ctx context.Context) (resourcev2.FeatureSet, error) {
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Features{Features: &resourcepb.FeatureRequest{}}})
	if err != nil {
		return resourcev2.FeatureSet{}, err
	}
	p := out.GetFeatures()
	if p == nil {
		return resourcev2.FeatureSet{}, invalid("unexpected feature result")
	}
	features := resourcev2.FeatureSet{Traits: make([]resourcev2.Feature, len(p.Traits))}
	for i, trait := range p.Traits {
		features.Traits[i] = resourcev2.Feature(trait)
	}
	if err := resourcev2.ValidateFeatures(features); err != nil {
		return resourcev2.FeatureSet{}, err
	}
	return features, nil
}
