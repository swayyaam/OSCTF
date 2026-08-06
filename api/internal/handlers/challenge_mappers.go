package handlers

import (
	"encoding/json"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/db/gen"
)

func decodeEnv(raw []byte) map[string]string {
	env := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	return env
}

func decodeStrings(raw []byte) []string {
	out := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func i32ptrToInt(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func diffPtr(p *string) *apigen.Difficulty {
	if p == nil {
		return nil
	}
	d := apigen.Difficulty(*p)
	return &d
}

func toAttachmentAdmin(a gen.ChallengeAttachment) apigen.AttachmentAdmin {
	return apigen.AttachmentAdmin{
		Id: a.ID, Filename: a.Filename, SizeBytes: a.SizeBytes,
		ContentType: a.ContentType, CreatedAt: a.CreatedAt,
	}
}

func toChallengeAdmin(f challenges.Full) apigen.ChallengeAdmin {
	c := f.Challenge
	atts := make([]apigen.AttachmentAdmin, 0, len(f.Attachments))
	for _, a := range f.Attachments {
		atts = append(atts, toAttachmentAdmin(a))
	}
	return apigen.ChallengeAdmin{
		Id:                  c.ID,
		Slug:                c.Slug,
		Title:               c.Title,
		Category:            apigen.Category(c.Category),
		Description:         c.Description,
		Difficulty:          diffPtr(c.Difficulty),
		Kind:                apigen.ChallengeKind(c.Kind),
		Type:                c.Type,
		Flag:                c.Flag,
		FlagCaseInsensitive: c.FlagCaseInsensitive,
		Scoring:             apigen.ScoringMode(c.Scoring),
		PointsInitial:       int(c.PointsInitial),
		PointsMin:           i32ptrToInt(c.PointsMin),
		Decay:               i32ptrToInt(c.Decay),
		MaxAttempts:         i32ptrToInt(c.MaxAttempts),
		Visible:             c.Visible,
		Image:               c.Image,
		InternalPort:        i32ptrToInt(c.InternalPort),
		MemLimitMb:          int(c.MemLimitMb),
		CpuMillis:           int(c.CpuMillis),
		ContainerEnv:        decodeEnv(c.ContainerEnv),
		ConnectionTemplate:  c.ConnectionTemplate,
		Instancing:          apigen.Instancing(c.Instancing),
		FlagMode:            apigen.FlagMode(c.FlagMode),
		InstanceTtlSeconds:  i32ptrToInt(c.InstanceTtlSeconds),
		Egress:              c.Egress,
		WritablePaths:       decodeStrings(c.WritablePaths),
		Attachments:         atts,
		Solves:              f.Solves,
		CurrentPoints:       challenges.CurrentPoints(c, f.Solves),
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
	}
}

// toChallenge maps a participant board entry. connectionInfo/hasInstance are
// filled by the runtime in M7; here they are empty/false.
func toChallenge(e challenges.BoardEntry) apigen.Challenge {
	c := e.Challenge
	return apigen.Challenge{
		Id:          c.ID,
		Slug:        c.Slug,
		Title:       c.Title,
		Category:    apigen.Category(c.Category),
		Difficulty:  diffPtr(c.Difficulty),
		Points:      e.Points,
		Solves:      e.Solves,
		SolvedByMe:  e.SolvedByMe,
		Kind:        apigen.ChallengeKind(c.Kind),
		Instancing:  apigen.Instancing(c.Instancing),
		HasInstance: false,
	}
}

func toChallengeDetail(d challenges.Detail) apigen.ChallengeDetail {
	c := d.Challenge
	atts := make([]apigen.Attachment, 0, len(d.Attachments))
	for _, a := range d.Attachments {
		atts = append(atts, apigen.Attachment{Id: a.ID, Filename: a.Filename, SizeBytes: a.SizeBytes})
	}
	return apigen.ChallengeDetail{
		Id:           c.ID,
		Slug:         c.Slug,
		Title:        c.Title,
		Category:     apigen.Category(c.Category),
		Difficulty:   diffPtr(c.Difficulty),
		Points:       d.Points,
		Solves:       d.Solves,
		SolvedByMe:   d.SolvedByMe,
		Kind:         apigen.ChallengeKind(c.Kind),
		Instancing:   apigen.Instancing(c.Instancing),
		FlagMode:     apigen.FlagMode(c.FlagMode),
		HasInstance:  false,
		Description:  c.Description,
		Attachments:  atts,
		MaxAttempts:  i32ptrToInt(c.MaxAttempts),
		AttemptsUsed: d.AttemptsUsed,
	}
}
