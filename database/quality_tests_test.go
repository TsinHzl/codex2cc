package database

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQualityTestSlotsAreAtomicAndReleaseOnlyAfterCompletion(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var admitted []QualityTestJob
	for i := int64(1); i <= 18; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			job, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: id, AccountName: "snapshot", Model: "gpt-5.5", ReasoningEffort: "high", Prompt: "HTML"})
			if err == nil {
				mu.Lock()
				admitted = append(admitted, *job)
				mu.Unlock()
			} else if !errors.Is(err, ErrQualityTestCapacity) {
				t.Errorf("admission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if len(admitted) != 3 {
		t.Fatalf("admitted %d jobs, want exactly 3", len(admitted))
	}
	job := admitted[0]
	if _, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: job.AccountID}); !errors.Is(err, ErrQualityTestAccountBusy) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := db.CancelQualityTest(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 100}); !errors.Is(err, ErrQualityTestCapacity) {
		t.Fatalf("cancel released capacity too early: %v", err)
	}
	job.Output = "<html><svg/></html>"
	job.Status = "completed"
	if err := db.SaveQualityTestProgress(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishQualityTest(ctx, job); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetQualityTestJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "stopped" || stored.Output != job.Output || stored.CompletedAt == nil {
		t.Fatalf("cancel lost to completion: %+v", stored)
	}
	if _, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestQualityTestRecordsPersistWithMetadataAndExcludeOutputFromLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 42, AccountName: "Historical name", PlanType: "pro", Channel: "codex", Model: "gpt-5.5", ReasoningEffort: "high", Prompt: "line 1\nline 2"})
	if err != nil {
		t.Fatal(err)
	}
	job.Output = "<html>鹈鹕</html>"
	job.Status = "completed"
	job.DurationMS = 1234
	if err := db.FinishQualityTest(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.GetQualityTestJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Output != job.Output || stored.AccountName != "Historical name" || stored.ReasoningEffort != "high" || stored.Model != "gpt-5.5" || stored.CreatedAt.IsZero() || stored.CompletedAt == nil || stored.DurationMS != 1234 {
		t.Fatalf("metadata/result did not persist: %+v", stored)
	}
	page, err := db.ListQualityTests(ctx, 1, 20, QualityTestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.ActiveJobs) != 0 || page.Jobs[0].Output != "" || page.Jobs[0].Prompt != "" {
		t.Fatalf("unexpected history: %+v", page)
	}
	active, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 43})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ExpireQualityTests(ctx, active.DeadlineAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	active, err = db.GetQualityTestJob(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "interrupted" || active.CompletedAt == nil {
		t.Fatalf("orphan was not recovered: %+v", active)
	}
}

func TestQualityTestHistoryFiltersCombineAndFacetsStayGlobal(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	seed := []QualityTestJob{
		{AccountID: 1, AccountName: "old name", PlanType: "pro", Model: "gpt-5.5", ReasoningEffort: "low"},
		{AccountID: 1, AccountName: "alice", PlanType: "pro", Model: "gpt-5.5", ReasoningEffort: "high"},
		{AccountID: 2, AccountName: "bob", PlanType: "plus", Model: "gpt-5.5", ReasoningEffort: "high"},
		{AccountID: 3, AccountName: "carol", PlanType: "team", Model: "gpt-6-astra", ReasoningEffort: "", PresetKind: "builtin", PresetRef: "clock", PresetName: "Clock"},
	}
	for _, item := range seed {
		job, err := db.CreateQualityTestJob(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		job.Status = "completed"
		if err := db.FinishQualityTest(ctx, *job); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.ListQualityTests(ctx, 1, 20, QualityTestFilter{Model: "gpt-5.5", ReasoningEffort: "high", HasEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Jobs) != 2 || page.Jobs[0].AccountID != 2 || page.Jobs[1].AccountID != 1 {
		t.Fatalf("model+effort combination: total=%d jobs=%+v", page.Total, page.Jobs)
	}
	page, err = db.ListQualityTests(ctx, 1, 20, QualityTestFilter{PlanType: "pro", AccountID: 1, Model: "gpt-5.5", ReasoningEffort: "low", HasEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Jobs[0].ReasoningEffort != "low" {
		t.Fatalf("all four filters: total=%d jobs=%+v", page.Total, page.Jobs)
	}
	page, err = db.ListQualityTests(ctx, 1, 20, QualityTestFilter{HasEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Jobs[0].Model != "gpt-6-astra" {
		t.Fatalf("model-default effort filter: total=%d jobs=%+v", page.Total, page.Jobs)
	}
	if len(page.Facets.Plans) != 3 || len(page.Facets.Models) != 2 || len(page.Facets.Efforts) != 3 || len(page.Facets.Accounts) != 3 {
		t.Fatalf("facets must ignore the active filter: %+v", page.Facets)
	}
	if page.Facets.Accounts[0].Name != "alice" || page.Facets.Accounts[0].ID != 1 {
		t.Fatalf("account facet should use the latest snapshot name: %+v", page.Facets.Accounts)
	}
	if len(page.Facets.Presets) != 1 || page.Facets.Presets[0] != (QualityTestPresetFacet{Kind: "builtin", Ref: "clock", Name: "Clock"}) {
		t.Fatalf("preset facet: %+v", page.Facets.Presets)
	}
	page, err = db.ListQualityTests(ctx, 1, 20, QualityTestFilter{HasPreset: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("hand-typed prompts: total=%d", page.Total)
	}
	page, err = db.ListQualityTests(ctx, 1, 20, QualityTestFilter{HasPreset: true, PresetKind: "builtin", PresetRef: "clock", Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Jobs[0].PresetName != "Clock" || page.Jobs[0].PresetKind != "builtin" {
		t.Fatalf("preset+model filter: total=%d jobs=%+v", page.Total, page.Jobs)
	}
}

func TestQualityTestSchemaAddsPresetColumnsToExistingTables(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, column := range []string{"preset_kind", "preset_ref", "preset_name"} {
		if _, err := db.conn.ExecContext(ctx, "ALTER TABLE quality_test_jobs DROP COLUMN "+column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ensureQualityTestSchema(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 9, PresetKind: "custom", PresetRef: "4", PresetName: "月球企鹅"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetQualityTestJob(ctx, job.ID)
	if err != nil || stored.PresetKind != "custom" || stored.PresetRef != "4" || stored.PresetName != "月球企鹅" {
		t.Fatalf("preset provenance after migration: %+v err=%v", stored, err)
	}
}
