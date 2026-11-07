package pricing

import "time"

// Revision 是一条从 ValidFrom 起生效的规则。ID 为 0 表示尚未落库。
type Revision struct {
	ID          int64
	ValidFrom   time.Time
	Source      Source
	Fingerprint string
	Text        string
	Catalog     Catalog
}

// NewRevision 从规则原文构造修订。原文坏掉时 Catalog 为空：
// 坏规则只影响新记录，不破坏已有历史的计价能力。
func NewRevision(id int64, validFrom time.Time, source Source, fingerprint, text string) Revision {
	strategy, _ := Parse(text)
	return Revision{
		ID:          id,
		ValidFrom:   validFrom,
		Source:      source,
		Fingerprint: fingerprint,
		Text:        text,
		Catalog:     NewCatalog(strategy),
	}
}

// Timeline 是按生效时刻排序的修订列表。
type Timeline struct {
	Revisions []Revision
}

func NewTimeline(revisions []Revision) Timeline {
	sorted := append([]Revision(nil), revisions...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].ValidFrom.Before(sorted[j-1].ValidFrom); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return Timeline{Revisions: sorted}
}

func (t Timeline) IsEmpty() bool { return len(t.Revisions) == 0 }

func (t Timeline) Latest() (Revision, bool) {
	if len(t.Revisions) == 0 {
		return Revision{}, false
	}
	return t.Revisions[len(t.Revisions)-1], true
}

// Effective 返回 at 时刻生效的修订：ValidFrom <= at 里最新的一条。
// 早于所有修订时使用最早一条，让迁移进来的老数据仍然能算出钱。
func (t Timeline) Effective(at time.Time) (Revision, bool) {
	if len(t.Revisions) == 0 {
		return Revision{}, false
	}
	result := t.Revisions[0]
	for _, revision := range t.Revisions {
		if revision.ValidFrom.After(at) {
			break
		}
		result = revision
	}
	return result, true
}

func (t Timeline) RevisionID(at time.Time) int64 {
	revision, ok := t.Effective(at)
	if !ok {
		return 0
	}
	return revision.ID
}

func (t Timeline) Cost(model string, promptTokens, completionTokens, cachedTokens int, at time.Time) float64 {
	revision, ok := t.Effective(at)
	if !ok {
		return 0
	}
	return revision.Catalog.Cost(model, promptTokens, completionTokens, cachedTokens, at)
}
