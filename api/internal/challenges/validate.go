package challenges

import (
	"fmt"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// validateCreate mirrors the DB CHECK constraints with precise messages.
func validateCreate(in CreateInput, slug string) error {
	v := apperr.NewValidation()
	if !slugRe.MatchString(slug) {
		v.Add("slug", "must be lowercase alphanumeric words separated by single hyphens")
	}
	if len(in.Title) < 1 || len(in.Title) > 200 {
		v.Add("title", "must be between 1 and 200 characters")
	}
	if !validCats[in.Category] {
		v.Add("category", "must be one of web, pwn, crypto, rev, forensics, misc")
	}
	if in.Difficulty != nil && !validDiffs[*in.Difficulty] {
		v.Add("difficulty", "must be one of intro, easy, medium, hard, insane")
	}
	if in.Kind != "standard" && in.Kind != "container" {
		v.Add("kind", "must be 'standard' or 'container'")
	}
	// Reject an unregistered challenge type at write time — an admin typo, or a challenge
	// authored against a plugin this deployment lacks, must fail on create rather than
	// storing a type nothing can resolve. The valid set is the runtime registry.
	if !IsRegisteredType(in.Type) {
		v.Add("type", fmt.Sprintf("unknown challenge type %q: no such type is registered", in.Type))
	}
	if len(in.Flag) < 1 {
		v.Add("flag", "is required")
	}
	if in.Scoring != "static" && in.Scoring != "dynamic" {
		v.Add("scoring", "must be 'static' or 'dynamic'")
	}
	if in.PointsInitial < 1 {
		v.Add("points_initial", "must be a positive integer")
	}
	if in.Scoring == "dynamic" {
		if in.PointsMin == nil || *in.PointsMin < 1 {
			v.Add("points_min", "is required and positive for dynamic scoring")
		} else if *in.PointsMin > in.PointsInitial {
			v.Add("points_min", "cannot exceed points_initial")
		}
		if in.Decay == nil || *in.Decay < 1 {
			v.Add("decay", "is required and positive for dynamic scoring")
		}
	}
	if in.Kind == "container" {
		if in.Image == nil || *in.Image == "" {
			v.Add("image", "is required for container challenges")
		}
		if in.InternalPort == nil || *in.InternalPort < 1 || *in.InternalPort > 65535 {
			v.Add("internal_port", "is required for container challenges (1-65535)")
		}
	}
	validateContainerLimits(v, in.MemLimitMB, in.CPUMillis)
	if in.MaxAttempts != nil && *in.MaxAttempts < 1 {
		v.Add("max_attempts", "must be positive when set")
	}
	validateInstancing(v, in.Kind, in.Instancing, in.FlagMode, in.InstanceTTLSeconds, in.Egress, len(in.WritablePaths) > 0)
	return v.OrNil()
}

// validateInstancing enforces that per_team / per_instance / ttl / egress-off /
// writable-paths are only valid for container challenges (mirrors the DB CHECK).
func validateInstancing(v *apperr.Validation, kind, instancing, flagMode string, ttl *int, egress *bool, hasWritable bool) {
	if instancing != "" && instancing != "shared" && instancing != "per_team" {
		v.Add("instancing", "must be 'shared' or 'per_team'")
	}
	if flagMode != "" && flagMode != "static" && flagMode != "per_instance" {
		v.Add("flag_mode", "must be 'static' or 'per_instance'")
	}
	if ttl != nil && *ttl < 0 {
		v.Add("instance_ttl_seconds", "must be >= 0")
	}
	if kind == "container" {
		return
	}
	if instancing == "per_team" {
		v.Add("instancing", "per_team requires a container challenge")
	}
	if flagMode == "per_instance" {
		v.Add("flag_mode", "per_instance requires a container challenge")
	}
	if ttl != nil {
		v.Add("instance_ttl_seconds", "only applies to container challenges")
	}
	if egress != nil && !*egress {
		v.Add("egress", "egress control only applies to container challenges")
	}
	if hasWritable {
		v.Add("writable_paths", "only apply to container challenges")
	}
}

// validateUpdate validates the row that would result from applying in to cur.
func validateUpdate(cur gen.Challenge, in UpdateInput) error {
	v := apperr.NewValidation()

	// Same write-time rejection on update: a changed type must be registered.
	if in.Type != nil && !IsRegisteredType(*in.Type) {
		v.Add("type", fmt.Sprintf("unknown challenge type %q: no such type is registered", *in.Type))
	}

	scoring := cur.Scoring
	if in.Scoring != nil {
		scoring = *in.Scoring
		if scoring != "static" && scoring != "dynamic" {
			v.Add("scoring", "must be 'static' or 'dynamic'")
		}
	}
	pointsInitial := int(cur.PointsInitial)
	if in.PointsInitial != nil {
		pointsInitial = *in.PointsInitial
		if pointsInitial < 1 {
			v.Add("points_initial", "must be a positive integer")
		}
	}
	pointsMin := i32val(cur.PointsMin)
	if in.SetPointsMin {
		pointsMin = valOrZero(in.PointsMin)
	}
	decay := i32val(cur.Decay)
	if in.SetDecay {
		decay = valOrZero(in.Decay)
	}
	if scoring == "dynamic" {
		if pointsMin < 1 {
			v.Add("points_min", "is required and positive for dynamic scoring")
		} else if pointsMin > pointsInitial {
			v.Add("points_min", "cannot exceed points_initial")
		}
		if decay < 1 {
			v.Add("decay", "is required and positive for dynamic scoring")
		}
	}

	if in.Slug != nil && !slugRe.MatchString(*in.Slug) {
		v.Add("slug", "must be lowercase alphanumeric words separated by single hyphens")
	}
	if in.Category != nil && !validCats[*in.Category] {
		v.Add("category", "must be one of web, pwn, crypto, rev, forensics, misc")
	}
	if in.SetDifficulty && in.Difficulty != nil && !validDiffs[*in.Difficulty] {
		v.Add("difficulty", "must be one of intro, easy, medium, hard, insane")
	}
	if in.Title != nil && (len(*in.Title) < 1 || len(*in.Title) > 200) {
		v.Add("title", "must be between 1 and 200 characters")
	}
	if in.Flag != nil && len(*in.Flag) < 1 {
		v.Add("flag", "cannot be empty")
	}

	// Container-kind invariants: a container challenge must keep image + port.
	if cur.Kind == "container" {
		image := cur.Image
		if in.SetImage {
			image = in.Image
		}
		port := cur.InternalPort
		if in.SetInternalPort {
			port = i32p(in.InternalPort)
		}
		if image == nil || *image == "" {
			v.Add("image", "a container challenge must keep an image")
		}
		if port == nil {
			v.Add("internal_port", "a container challenge must keep an internal port")
		}
	}
	validateContainerLimits(v, in.MemLimitMB, in.CPUMillis)

	// Resolve instancing/flag_mode/egress/writable against the current row.
	instancing := cur.Instancing
	if in.Instancing != nil {
		instancing = *in.Instancing
	}
	flagMode := cur.FlagMode
	if in.FlagMode != nil {
		flagMode = *in.FlagMode
	}
	var ttl *int
	if in.SetInstanceTTL {
		ttl = in.InstanceTTLSeconds
	} else if cur.InstanceTtlSeconds != nil {
		t := int(*cur.InstanceTtlSeconds)
		ttl = &t
	}
	egress := cur.Egress
	if in.Egress != nil {
		egress = *in.Egress
	}
	// A standard challenge cannot already have writable paths (DB CHECK), so only
	// the incoming patch can introduce them.
	validateInstancing(v, cur.Kind, instancing, flagMode, ttl, &egress, len(in.WritablePaths) > 0)
	return v.OrNil()
}

func validateContainerLimits(v *apperr.Validation, mem, cpu *int) {
	if mem != nil && (*mem < 16 || *mem > 8192) {
		v.Add("mem_limit_mb", "must be between 16 and 8192")
	}
	if cpu != nil && (*cpu < 100 || *cpu > 8000) {
		v.Add("cpu_millis", "must be between 100 and 8000")
	}
}

func i32val(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func valOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
