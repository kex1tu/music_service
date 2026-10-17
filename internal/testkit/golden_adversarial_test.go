package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ==============================================================================
// 4. AssertGoldenJSON Adversarial Stress Tests
// ==============================================================================

func TestAdversarial_AssertGoldenJSON_ExactMatches(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Exact byte-for-byte match
	t.Run("exact_bytes", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "exact.golden.json")
		content := []byte("{\n  \"album\": \"Abbey Road\",\n  \"artist\": \"The Beatles\",\n  \"year\": 1969\n}\n")
		if err := os.WriteFile(goldenFile, content, 0644); err != nil {
			t.Fatal(err)
		}

		AssertGoldenJSON(t, content, goldenFile)
	})

	// 2. Semantic match with compact formatting and opposite key ordering
	t.Run("semantic_match_different_keys_and_spaces", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "reorder.golden.json")
		expectedNormalized := []byte("{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n")
		if err := os.WriteFile(goldenFile, expectedNormalized, 0644); err != nil {
			t.Fatal(err)
		}

		// Actual has reverse order and minified spacing
		actual := []byte(`{"c":3,"b":2,"a":1}`)
		AssertGoldenJSON(t, actual, goldenFile)
	})

	// 3. Deeply nested structure semantic match
	t.Run("deeply_nested_semantic_match", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "nested.golden.json")
		actual := []byte(`{
			"root": {
				"level1": {
					"level2": {
						"items": [1, 2, 3],
						"flag": true
					}
				}
			}
		}`)

		AssertGoldenJSON(t, actual, goldenFile)

		// Calling it again on the created file must succeed identically
		AssertGoldenJSON(t, actual, goldenFile)
	})
}

func TestAdversarial_AssertGoldenJSON_LargeNumbersAndUseNumber(t *testing.T) {
	tempDir := t.TempDir()
	goldenFile := filepath.Join(tempDir, "large_numbers.golden.json")

	// Numbers beyond 2^53 (9007199254740992) lose precision in float64
	// float64(9007199254740993) == 9007199254740992
	// float64(9223372036854775807) == 9223372036854775808
	largeInt1 := "9007199254740993"
	maxInt64Str := "9223372036854775807"
	minInt64Str := "-9223372036854775808"
	preciseFloat := "123456789.987654321"

	payload := []byte(`{
  "large_id": ` + largeInt1 + `,
  "max_int64": ` + maxInt64Str + `,
  "min_int64": ` + minInt64Str + `,
  "precise_float": ` + preciseFloat + `
}`)

	// 1. Test normalizeJSON preserves exact string representation of large numbers
	normalized, err := normalizeJSON(payload)
	if err != nil {
		t.Fatalf("normalizeJSON failed: %v", err)
	}

	normalizedStr := string(normalized)
	if !strings.Contains(normalizedStr, `"large_id": `+largeInt1) {
		t.Errorf("Precision lost for large_id! Got:\n%s", normalizedStr)
	}
	if !strings.Contains(normalizedStr, `"max_int64": `+maxInt64Str) {
		t.Errorf("Precision lost for max_int64! Got:\n%s", normalizedStr)
	}
	if !strings.Contains(normalizedStr, `"min_int64": `+minInt64Str) {
		t.Errorf("Precision lost for min_int64! Got:\n%s", normalizedStr)
	}

	// 2. Test AssertGoldenJSON end-to-end with large numbers
	AssertGoldenJSON(t, payload, goldenFile)

	// Read written file and verify digits
	fileBytes, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}

	fileStr := string(fileBytes)
	if !strings.Contains(fileStr, largeInt1) {
		t.Errorf("golden file lost precision for %s: %s", largeInt1, fileStr)
	}
	if !strings.Contains(fileStr, maxInt64Str) {
		t.Errorf("golden file lost precision for %s: %s", maxInt64Str, fileStr)
	}
	if !strings.Contains(fileStr, minInt64Str) {
		t.Errorf("golden file lost precision for %s: %s", minInt64Str, fileStr)
	}

	// 3. Compare without modification - must match without precision drift
	AssertGoldenJSON(t, payload, goldenFile)
}

func TestAdversarial_AssertGoldenJSON_CompactDiffFormatting(t *testing.T) {
	// Test formatJSONDiff directly with multiple scenarios

	// Scenario A: Single field change in the middle
	t.Run("single_field_diff", func(t *testing.T) {
		want := []byte("{\n  \"album\": \"Revolver\",\n  \"artist\": \"Beatles\",\n  \"year\": 1966\n}\n")
		got := []byte("{\n  \"album\": \"Revolver\",\n  \"artist\": \"The Beatles\",\n  \"year\": 1966\n}\n")

		diff := formatJSONDiff("/path/to/revolver.golden.json", want, got)

		if !strings.Contains(diff, "--- WANT (/path/to/revolver.golden.json)") {
			t.Errorf("missing WANT header: %s", diff)
		}
		if !strings.Contains(diff, "+++ GOT (actual)") {
			t.Errorf("missing GOT header: %s", diff)
		}
		if !strings.Contains(diff, "@@ line 3 @@") {
			t.Errorf("expected @@ line 3 @@ in diff, got: %s", diff)
		}
		if !strings.Contains(diff, "-   \"artist\": \"Beatles\"") {
			t.Errorf("expected minus line, got: %s", diff)
		}
		if !strings.Contains(diff, "+   \"artist\": \"The Beatles\"") {
			t.Errorf("expected plus line, got: %s", diff)
		}
	})

	// Scenario B: Added lines at the end
	t.Run("added_lines_diff", func(t *testing.T) {
		want := []byte("{\n  \"id\": 1\n}\n")
		got := []byte("{\n  \"id\": 1,\n  \"name\": \"new_item\"\n}\n")

		diff := formatJSONDiff("test.json", want, got)
		if !strings.Contains(diff, "+   \"name\": \"new_item\"") {
			t.Errorf("expected added line in diff: %s", diff)
		}
	})

	// Scenario C: Identical lines
	t.Run("identical_lines", func(t *testing.T) {
		same := []byte("{\n  \"id\": 1\n}\n")
		diff := formatJSONDiff("test.json", same, same)
		if !strings.Contains(diff, "no line-by-line differences detected") {
			t.Errorf("expected 'no line-by-line differences detected', got: %s", diff)
		}
	})
}

func TestAdversarial_AssertGoldenJSON_UpdateGoldenFlags(t *testing.T) {
	tempDir := t.TempDir()

	// 1. UPDATE_GOLDEN=true creates new file
	t.Run("create_with_update_golden_true", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "new_dir", "new_file.golden.json")
		t.Setenv("UPDATE_GOLDEN", "true")

		payload := []byte(`{"created":true,"status":"ok"}`)
		AssertGoldenJSON(t, payload, goldenFile)

		fi, err := os.Stat(goldenFile)
		if err != nil {
			t.Fatalf("expected file to be created: %v", err)
		}
		if fi.Mode().Perm() != 0644 {
			t.Errorf("expected 0644 file permission, got %v", fi.Mode().Perm())
		}

		data, _ := os.ReadFile(goldenFile)
		expected := "{\n  \"created\": true,\n  \"status\": \"ok\"\n}\n"
		if string(data) != expected {
			t.Errorf("content mismatch:\nWANT:\n%s\nGOT:\n%s", expected, string(data))
		}
	})

	// 2. UPDATE_GOLDEN=1 updates existing file
	t.Run("overwrite_with_update_golden_1", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "existing.golden.json")
		if err := os.WriteFile(goldenFile, []byte("{\n  \"version\": 1\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		t.Setenv("UPDATE_GOLDEN", "1")
		newPayload := []byte(`{"version":2,"updated":true}`)
		AssertGoldenJSON(t, newPayload, goldenFile)

		data, _ := os.ReadFile(goldenFile)
		expected := "{\n  \"updated\": true,\n  \"version\": 2\n}\n"
		if string(data) != expected {
			t.Errorf("content mismatch:\nWANT:\n%s\nGOT:\n%s", expected, string(data))
		}
	})

	// 3. Subprocess verification of CI mode failure when golden file is missing
	t.Run("ci_missing_file_failure", func(t *testing.T) {
		if os.Getenv("TEST_GOLDEN_SUBPROCESS") == "1" {
			t.Setenv("CI", "true")
			t.Setenv("UPDATE_GOLDEN", "")
			AssertGoldenJSON(t, []byte(`{"a":1}`), filepath.Join(tempDir, "does_not_exist.json"))
			return
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestAdversarial_AssertGoldenJSON_UpdateGoldenFlags/ci_missing_file_failure")
		cmd.Env = append(os.Environ(), "TEST_GOLDEN_SUBPROCESS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected test failure in CI environment when golden file is missing, but subprocess passed")
		}
		outStr := string(out)
		if !strings.Contains(outStr, "golden file") || !strings.Contains(outStr, "does not exist in CI environment") {
			t.Errorf("unexpected error message from CI missing file: %s", outStr)
		}
	})

	// 4. Subprocess verification of mismatch failure with diff
	t.Run("mismatch_failure_with_diff", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "mismatch_test.golden.json")
		if err := os.WriteFile(goldenFile, []byte("{\n  \"mode\": \"production\"\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		if os.Getenv("TEST_GOLDEN_MISMATCH_SUBPROCESS") == "1" {
			t.Setenv("UPDATE_GOLDEN", "")
			AssertGoldenJSON(t, []byte(`{"mode":"development"}`), goldenFile)
			return
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestAdversarial_AssertGoldenJSON_UpdateGoldenFlags/mismatch_failure_with_diff")
		cmd.Env = append(os.Environ(), "TEST_GOLDEN_MISMATCH_SUBPROCESS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected test failure on mismatch, but subprocess passed")
		}
		outStr := string(out)
		if !strings.Contains(outStr, "golden file mismatch") {
			t.Errorf("expected 'golden file mismatch' in error output, got: %s", outStr)
		}
		if !strings.Contains(outStr, "-   \"mode\": \"production\"") {
			t.Errorf("expected removed line in diff output, got: %s", outStr)
		}
		if !strings.Contains(outStr, "+   \"mode\": \"development\"") {
			t.Errorf("expected added line in diff output, got: %s", outStr)
		}
	})

	// 5. Subprocess verification of empty actual JSON payload
	t.Run("empty_actual_payload_failure", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "empty_test.golden.json")
		if os.Getenv("TEST_GOLDEN_EMPTY_SUBPROCESS") == "1" {
			AssertGoldenJSON(t, []byte("   \n\t  "), goldenFile)
			return
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestAdversarial_AssertGoldenJSON_UpdateGoldenFlags/empty_actual_payload_failure")
		cmd.Env = append(os.Environ(), "TEST_GOLDEN_EMPTY_SUBPROCESS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected test failure on empty actual payload, but subprocess passed")
		}
		outStr := string(out)
		if !strings.Contains(outStr, "actual JSON payload is empty") {
			t.Errorf("expected 'actual JSON payload is empty' in output, got: %s", outStr)
		}
	})

	// 6. Subprocess verification of invalid JSON in golden file
	t.Run("invalid_json_in_golden_file", func(t *testing.T) {
		goldenFile := filepath.Join(tempDir, "corrupted.golden.json")
		if err := os.WriteFile(goldenFile, []byte("{invalid-json-content"), 0644); err != nil {
			t.Fatal(err)
		}

		if os.Getenv("TEST_GOLDEN_CORRUPT_SUBPROCESS") == "1" {
			t.Setenv("UPDATE_GOLDEN", "")
			AssertGoldenJSON(t, []byte(`{"valid":true}`), goldenFile)
			return
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestAdversarial_AssertGoldenJSON_UpdateGoldenFlags/invalid_json_in_golden_file")
		cmd.Env = append(os.Environ(), "TEST_GOLDEN_CORRUPT_SUBPROCESS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected test failure on corrupted golden file, but subprocess passed")
		}
		outStr := string(out)
		if !strings.Contains(outStr, "contains invalid JSON") {
			t.Errorf("expected 'contains invalid JSON' in output, got: %s", outStr)
		}
	})
}
