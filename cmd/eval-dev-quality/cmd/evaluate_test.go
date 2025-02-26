package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zimmski/osutil"

	"github.com/symflower/eval-dev-quality/evaluate"
	"github.com/symflower/eval-dev-quality/evaluate/metrics"
	metricstesting "github.com/symflower/eval-dev-quality/evaluate/metrics/testing"
	evaluatetask "github.com/symflower/eval-dev-quality/evaluate/task"
	"github.com/symflower/eval-dev-quality/language"
	"github.com/symflower/eval-dev-quality/log"
	"github.com/symflower/eval-dev-quality/model"
	providertesting "github.com/symflower/eval-dev-quality/provider/testing"
	"github.com/symflower/eval-dev-quality/tools"
	toolstesting "github.com/symflower/eval-dev-quality/tools/testing"
)

// validateReportLinks checks if the Markdown report data contains all the links to other relevant report files.
func validateReportLinks(t *testing.T, data string, modelLogNames []string) {
	assert.Contains(t, data, "](./categories.svg)")
	assert.Contains(t, data, "](./evaluation.csv)")
	assert.Contains(t, data, "](./evaluation.log)")
	for _, m := range modelLogNames {
		assert.Contains(t, data, fmt.Sprintf("](./%s/)", m))
	}
}

// validateSVGContent checks if the SVG data contains all given categories and an axis label for the maximal model count.
func validateSVGContent(t *testing.T, data string, categories []*metrics.AssessmentCategory, maxModelCount uint) {
	for _, category := range categories {
		assert.Contains(t, data, fmt.Sprintf("%s</text>", category.Name))
	}
	assert.Contains(t, data, fmt.Sprintf("%d</text>", maxModelCount))
}

func atoiUint64(t *testing.T, s string) uint64 {
	value, err := strconv.ParseUint(s, 10, 64)
	assert.NoErrorf(t, err, "parsing unsigned integer from: %q", s)

	return uint64(value)
}

// extractMetricsMatch is a regular expression that maps metrics to it's subgroups.
type extractMetricsMatch *regexp.Regexp

// extractMetricsLogsMatch is a regular expression to extract metrics from log messages.
var extractMetricsLogsMatch = extractMetricsMatch(regexp.MustCompile(`score=(\d+), coverage=(\d+), files-executed=(\d+), generate-tests-for-file-character-count=(\d+), processing-time=(\d+), response-character-count=(\d+), response-no-error=(\d+), response-no-excess=(\d+), response-with-code=(\d+)`))

// extractMetricsCSVMatch is a regular expression to extract metrics from CSV rows.
// REMARK The cost is not match as a group since it's just a model property that we carry along for informational purposes.
var extractMetricsCSVMatch = extractMetricsMatch(regexp.MustCompile(`(?:\d+(?:\.\d+)?,)?(\d+),(\d+),(\d+),(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)`))

// extractMetrics extracts multiple assessment metrics from the given string according to a given regular expression.
func extractMetrics(t *testing.T, regex extractMetricsMatch, data string) (assessments []metrics.Assessments, scores []uint64) {
	matches := (*regexp.Regexp)(regex).FindAllStringSubmatch(data, -1)

	for _, match := range matches {
		assessments = append(assessments, metrics.Assessments{
			metrics.AssessmentKeyCoverage:                           atoiUint64(t, match[2]),
			metrics.AssessmentKeyFilesExecuted:                      atoiUint64(t, match[3]),
			metrics.AssessmentKeyGenerateTestsForFileCharacterCount: atoiUint64(t, match[4]),
			metrics.AssessmentKeyProcessingTime:                     atoiUint64(t, match[5]),
			metrics.AssessmentKeyResponseCharacterCount:             atoiUint64(t, match[6]),
			metrics.AssessmentKeyResponseNoError:                    atoiUint64(t, match[7]),
			metrics.AssessmentKeyResponseNoExcess:                   atoiUint64(t, match[8]),
			metrics.AssessmentKeyResponseWithCode:                   atoiUint64(t, match[9]),
		})
		scores = append(scores, atoiUint64(t, match[1]))
	}

	return assessments, scores
}

func validateMetrics(t *testing.T, regex *regexp.Regexp, data string, expectedAssessments []metrics.Assessments, expectedScores []uint64) (actual []metrics.Assessments) {
	require.Equal(t, len(expectedAssessments), len(expectedScores), "expected assessment and scores length")

	actualAssessments, actualScores := extractMetrics(t, regex, data)
	require.Equal(t, len(expectedAssessments), len(actualAssessments), "expected and actual assessment length")
	for i := range actualAssessments {
		metricstesting.AssertAssessmentsEqual(t, expectedAssessments[i], actualAssessments[i])
	}
	assert.Equal(t, expectedScores, actualScores)

	return actualAssessments
}

func TestEvaluateExecute(t *testing.T) {
	if osutil.IsLinux() {
		toolstesting.RequiresTool(t, tools.NewOllama())
	}
	toolstesting.RequiresTool(t, tools.NewSymflower())

	type testCase struct {
		Name string

		Before func(t *testing.T, logger *log.Logger, resultPath string)
		After  func(t *testing.T, logger *log.Logger, resultPath string)

		Arguments []string

		ExpectedOutputValidate func(t *testing.T, output string, resultPath string)
		ExpectedResultFiles    map[string]func(t *testing.T, filePath string, data string)
		ExpectedPanicContains  string
	}

	validate := func(t *testing.T, tc *testCase) {
		t.Run(tc.Name, func(t *testing.T) {
			temporaryPath := t.TempDir()

			logOutput, logger := log.Buffer()
			defer func() {
				if t.Failed() {
					t.Logf("Logging output: %s", logOutput.String())
				}
			}()

			if tc.Before != nil {
				tc.Before(t, logger, temporaryPath)
			}
			if tc.After != nil {
				defer tc.After(t, logger, temporaryPath)
			}

			arguments := append([]string{
				"evaluate",
				"--result-path", filepath.Join(temporaryPath, "result-directory"),
				"--testdata", filepath.Join("..", "..", "..", "testdata"),
			}, tc.Arguments...)

			if tc.ExpectedPanicContains == "" {
				assert.NotPanics(t, func() {
					Execute(logger, arguments)
				})
			} else {
				didPanic := true
				var recovered any
				func() {
					defer func() {
						recovered = recover()
					}()

					Execute(logger, arguments)

					didPanic = false
				}()
				assert.True(t, didPanic)
				assert.Contains(t, recovered, tc.ExpectedPanicContains)
			}

			if tc.ExpectedOutputValidate != nil {
				tc.ExpectedOutputValidate(t, logOutput.String(), temporaryPath)
			}

			actualResultFiles, err := osutil.FilesRecursive(temporaryPath)
			require.NoError(t, err)
			for i, p := range actualResultFiles {
				actualResultFiles[i], err = filepath.Rel(temporaryPath, p)
				require.NoError(t, err)
			}
			sort.Strings(actualResultFiles)
			expectedResultFiles := make([]string, 0, len(tc.ExpectedResultFiles))
			for filePath, validate := range tc.ExpectedResultFiles {
				expectedResultFiles = append(expectedResultFiles, filePath)

				if validate != nil {
					data, err := os.ReadFile(filepath.Join(temporaryPath, filePath))
					if assert.NoError(t, err) {
						validate(t, filePath, string(data))
					}
				}
			}
			sort.Strings(expectedResultFiles)
			assert.Equal(t, expectedResultFiles, actualResultFiles)
		})
	}

	t.Run("Language filter", func(t *testing.T) {
		validate(t, &testCase{
			Name: "Single",

			Arguments: []string{
				"--language", "golang",
				"--model", "symflower/symbolic-execution",
				"--repository", filepath.Join("golang", "plain"),
			},

			ExpectedOutputValidate: func(t *testing.T, output string, resultPath string) {
				actualAssessments := validateMetrics(t, extractMetricsLogsMatch, output, []metrics.Assessments{
					metrics.Assessments{
						metrics.AssessmentKeyCoverage:         10,
						metrics.AssessmentKeyFilesExecuted:    1,
						metrics.AssessmentKeyResponseNoError:  1,
						metrics.AssessmentKeyResponseNoExcess: 1,
						metrics.AssessmentKeyResponseWithCode: 1,
					},
				}, []uint64{14})
				// Assert non-deterministic behavior.
				assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
				assert.Equal(t, 1, strings.Count(output, "Evaluation score for"))
			},
			ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
				filepath.Join("result-directory", "categories.svg"): func(t *testing.T, filePath, data string) {
					validateSVGContent(t, data, []*metrics.AssessmentCategory{metrics.AssessmentCategoryCodeNoExcess}, 1)
				},
				filepath.Join("result-directory", "evaluation.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
				},
				filepath.Join("result-directory", "evaluation.log"): nil,
				filepath.Join("result-directory", "golang-summed.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
				},
				filepath.Join("result-directory", "models-summed.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
				},
				filepath.Join("result-directory", "README.md"): func(t *testing.T, filePath, data string) {
					validateReportLinks(t, data, []string{"symflower_symbolic-execution"})
				},
				filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): nil,
			},
		})
		validate(t, &testCase{
			Name: "Multiple",

			Arguments: []string{
				"--model", "symflower/symbolic-execution",
				"--repository", filepath.Join("golang", "plain"),
				"--repository", filepath.Join("java", "plain"),
			},

			ExpectedOutputValidate: func(t *testing.T, output string, resultPath string) {
				actualAssessments := validateMetrics(t, extractMetricsLogsMatch, output, []metrics.Assessments{
					metrics.Assessments{
						metrics.AssessmentKeyCoverage:         20,
						metrics.AssessmentKeyFilesExecuted:    2,
						metrics.AssessmentKeyResponseNoError:  2,
						metrics.AssessmentKeyResponseNoExcess: 2,
						metrics.AssessmentKeyResponseWithCode: 2,
					},
				}, []uint64{28})
				// Assert non-deterministic behavior.
				assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(393))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(393))
				assert.Equal(t, 1, strings.Count(output, "Evaluation score for"))
			},
			ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
				filepath.Join("result-directory", "categories.svg"): func(t *testing.T, filePath, data string) {
					validateSVGContent(t, data, []*metrics.AssessmentCategory{metrics.AssessmentCategoryCodeNoExcess}, 1)
				},
				filepath.Join("result-directory", "evaluation.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14, 14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					assert.Greater(t, actualAssessments[1][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[1][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(139))
					assert.Equal(t, actualAssessments[1][metrics.AssessmentKeyResponseCharacterCount], uint64(139))
				},
				filepath.Join("result-directory", "golang-summed.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
				},
				filepath.Join("result-directory", "java-summed.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(139))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(139))
				},
				filepath.Join("result-directory", "models-summed.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         20,
							metrics.AssessmentKeyFilesExecuted:    2,
							metrics.AssessmentKeyResponseNoError:  2,
							metrics.AssessmentKeyResponseNoExcess: 2,
							metrics.AssessmentKeyResponseWithCode: 2,
						},
					}, []uint64{28})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(393))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(393))
				},
				filepath.Join("result-directory", "evaluation.log"): nil,
				filepath.Join("result-directory", "README.md"): func(t *testing.T, filePath, data string) {
					validateReportLinks(t, data, []string{"symflower_symbolic-execution"})
				},
				filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): func(t *testing.T, filePath, data string) {
					assert.Contains(t, data, "coverage objects: [{")
				},
				filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "java", "java", "plain.log"): func(t *testing.T, filePath, data string) {
					assert.Contains(t, data, "coverage objects: [{")
				},
			},
		})
	})

	t.Run("Repository filter", func(t *testing.T) {
		t.Run("Single", func(t *testing.T) {
			validate(t, &testCase{
				Name: "Single Language",

				Arguments: []string{
					"--language", "golang",
					"--model", "symflower/symbolic-execution",
					"--repository", filepath.Join("golang", "plain"),
				},

				ExpectedOutputValidate: func(t *testing.T, output string, resultPath string) {
					actualAssessments := validateMetrics(t, extractMetricsLogsMatch, output, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         10,
							metrics.AssessmentKeyFilesExecuted:    1,
							metrics.AssessmentKeyResponseNoError:  1,
							metrics.AssessmentKeyResponseNoExcess: 1,
							metrics.AssessmentKeyResponseWithCode: 1,
						},
					}, []uint64{14})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					assert.Equal(t, 1, strings.Count(output, "Evaluation score for"))
				},
				ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
					filepath.Join("result-directory", "categories.svg"): func(t *testing.T, filePath, data string) {
						validateSVGContent(t, data, []*metrics.AssessmentCategory{metrics.AssessmentCategoryCodeNoExcess}, 1)
					},
					filepath.Join("result-directory", "evaluation.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "evaluation.log"): nil,
					filepath.Join("result-directory", "golang-summed.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "models-summed.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "README.md"): func(t *testing.T, filePath, data string) {
						validateReportLinks(t, data, []string{"symflower_symbolic-execution"})
					},
					filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): nil,
				},
			})
			validate(t, &testCase{
				Name: "Multiple Languages",

				Arguments: []string{
					"--model", "symflower/symbolic-execution",
					"--repository", filepath.Join("golang", "plain"),
				},

				ExpectedOutputValidate: func(t *testing.T, output string, resultPath string) {
					assert.Regexp(t, `Evaluation score for "symflower/symbolic-execution" \("code-no-excess"\): cost=0.00, score=14, coverage=10, files-executed=1, generate-tests-for-file-character-count=254, processing-time=\d+, response-character-count=254, response-no-error=1, response-no-excess=1, response-with-code=1`, output)
					assert.Equal(t, 1, strings.Count(output, "Evaluation score for"))
				},
				ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
					filepath.Join("result-directory", "categories.svg"): func(t *testing.T, filePath, data string) {
						validateSVGContent(t, data, []*metrics.AssessmentCategory{metrics.AssessmentCategoryCodeNoExcess}, 1)
					},
					filepath.Join("result-directory", "evaluation.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "evaluation.log"): nil,
					filepath.Join("result-directory", "golang-summed.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "models-summed.csv"): func(t *testing.T, filePath, data string) {
						actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
							metrics.Assessments{
								metrics.AssessmentKeyCoverage:         10,
								metrics.AssessmentKeyFilesExecuted:    1,
								metrics.AssessmentKeyResponseNoError:  1,
								metrics.AssessmentKeyResponseNoExcess: 1,
								metrics.AssessmentKeyResponseWithCode: 1,
							},
						}, []uint64{14})
						// Assert non-deterministic behavior.
						assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(254))
						assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(254))
					},
					filepath.Join("result-directory", "README.md"): func(t *testing.T, filePath, data string) {
						validateReportLinks(t, data, []string{"symflower_symbolic-execution"})
					},
					filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): nil,
				},
			})
		})
	})
	t.Run("Model filter", func(t *testing.T) {
		t.Run("openrouter.ai", func(t *testing.T) {
			validate(t, &testCase{
				Name: "Unavailable",

				Arguments: []string{
					"--model", "openrouter/auto",
					"--tokens", "openrouter:",
				},

				ExpectedOutputValidate: func(t *testing.T, output, resultPath string) {
					assert.Contains(t, output, "Skipping unavailable provider \"openrouter\"")
				},
				ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
					filepath.Join("result-directory", "evaluation.log"): nil,
				},
				ExpectedPanicContains: "ERROR: model openrouter/auto does not exist",
			})
		})
		t.Run("Ollama", func(t *testing.T) {
			if !osutil.IsLinux() {
				t.Skipf("Installation of Ollama is not supported on this OS")
			}

			{
				var shutdown func() (err error)
				defer func() { // Defer the shutdown in case there is a panic.
					if shutdown != nil {
						require.NoError(t, shutdown())
					}
				}()
				validate(t, &testCase{
					Name: "Pulled Model",

					Before: func(t *testing.T, logger *log.Logger, resultPath string) {
						var err error
						shutdown, err = tools.OllamaStart(logger, tools.OllamaPath, tools.OllamaURL)
						require.NoError(t, err)

						require.NoError(t, tools.OllamaPull(logger, tools.OllamaPath, tools.OllamaURL, providertesting.OllamaTestModel))
					},

					Arguments: []string{
						"--language", "golang",
						"--model", "ollama/" + providertesting.OllamaTestModel,
						"--repository", filepath.Join("golang", "plain"),
					},

					ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
						filepath.Join("result-directory", "categories.svg"): nil,
						filepath.Join("result-directory", "evaluation.csv"): nil,
						filepath.Join("result-directory", "evaluation.log"): func(t *testing.T, filePath, data string) {
							// Since the model is non-deterministic, we can only assert that the model did at least not error.
							assert.Contains(t, data, fmt.Sprintf(`Evaluation score for "ollama/%s"`, providertesting.OllamaTestModel))
							assert.Contains(t, data, "response-no-error=1")
							assert.Contains(t, data, "preloading model")
							assert.Contains(t, data, "unloading model")
						},
						filepath.Join("result-directory", "golang-summed.csv"): nil,
						filepath.Join("result-directory", "models-summed.csv"): nil,
						filepath.Join("result-directory", "README.md"):         nil,
						filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "ollama_"+model.CleanModelNameForFileSystem(providertesting.OllamaTestModel), "golang", "golang", "plain.log"): nil,
					},
				})
			}
		})
		t.Run("OpenAI API", func(t *testing.T) {
			if !osutil.IsLinux() {
				t.Skipf("Installation of Ollama is not supported on this OS")
			}

			{
				var shutdown func() (err error)
				defer func() {
					if shutdown != nil {
						require.NoError(t, shutdown())
					}
				}()
				ollamaOpenAIAPIUrl, err := url.JoinPath(tools.OllamaURL, "v1")
				require.NoError(t, err)
				validate(t, &testCase{
					Name: "Ollama",

					Before: func(t *testing.T, logger *log.Logger, resultPath string) {
						var err error
						shutdown, err = tools.OllamaStart(logger, tools.OllamaPath, tools.OllamaURL)
						require.NoError(t, err)

						require.NoError(t, tools.OllamaPull(logger, tools.OllamaPath, tools.OllamaURL, providertesting.OllamaTestModel))
					},

					Arguments: []string{
						"--language", "golang",
						"--urls", "custom-ollama:" + ollamaOpenAIAPIUrl,
						"--model", "custom-ollama/" + providertesting.OllamaTestModel,
						"--repository", filepath.Join("golang", "plain"),
					},

					ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
						filepath.Join("result-directory", "categories.svg"): nil,
						filepath.Join("result-directory", "evaluation.csv"): nil,
						filepath.Join("result-directory", "evaluation.log"): func(t *testing.T, filePath, data string) {
							// Since the model is non-deterministic, we can only assert that the model did at least not error.
							assert.Contains(t, data, fmt.Sprintf(`Evaluation score for "custom-ollama/%s"`, providertesting.OllamaTestModel))
							assert.Contains(t, data, "response-no-error=1")
						},
						filepath.Join("result-directory", "golang-summed.csv"): nil,
						filepath.Join("result-directory", "models-summed.csv"): nil,
						filepath.Join("result-directory", "README.md"):         nil,
						filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "custom-ollama_"+model.CleanModelNameForFileSystem(providertesting.OllamaTestModel), "golang", "golang", "plain.log"): nil,
					},
				})
			}
		})
	})

	t.Run("Runs", func(t *testing.T) {
		validate(t, &testCase{
			Name: "Multiple",

			Arguments: []string{
				"--model", "symflower/symbolic-execution",
				"--repository", filepath.Join("golang", "plain"),
				"--runs=3",
			},

			ExpectedOutputValidate: func(t *testing.T, output string, resultPath string) {
				actualAssessments := validateMetrics(t, extractMetricsLogsMatch, output, []metrics.Assessments{
					metrics.Assessments{
						metrics.AssessmentKeyCoverage:         30,
						metrics.AssessmentKeyFilesExecuted:    3,
						metrics.AssessmentKeyResponseNoError:  3,
						metrics.AssessmentKeyResponseNoExcess: 3,
						metrics.AssessmentKeyResponseWithCode: 3,
					},
				}, []uint64{42})
				// Assert non-deterministic behavior.
				assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(762))
				assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(762))
				assert.Equal(t, 1, strings.Count(output, "Evaluation score for"))
			},
			ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
				filepath.Join("result-directory", "categories.svg"): nil,
				filepath.Join("result-directory", "evaluation.csv"): func(t *testing.T, filePath, data string) {
					actualAssessments := validateMetrics(t, extractMetricsCSVMatch, data, []metrics.Assessments{
						metrics.Assessments{
							metrics.AssessmentKeyCoverage:         30,
							metrics.AssessmentKeyFilesExecuted:    3,
							metrics.AssessmentKeyResponseNoError:  3,
							metrics.AssessmentKeyResponseNoExcess: 3,
							metrics.AssessmentKeyResponseWithCode: 3,
						},
					}, []uint64{42})
					// Assert non-deterministic behavior.
					assert.Greater(t, actualAssessments[0][metrics.AssessmentKeyProcessingTime], uint64(0))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyGenerateTestsForFileCharacterCount], uint64(762))
					assert.Equal(t, actualAssessments[0][metrics.AssessmentKeyResponseCharacterCount], uint64(762))
				},
				filepath.Join("result-directory", "evaluation.log"): func(t *testing.T, filePath, data string) {
					assert.Contains(t, data, "Run 1/3")
					assert.Contains(t, data, "Run 2/3")
					assert.Contains(t, data, "Run 3/3")
				},
				filepath.Join("result-directory", "golang-summed.csv"): nil,
				filepath.Join("result-directory", "models-summed.csv"): nil,
				filepath.Join("result-directory", "README.md"):         nil,
				filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): func(t *testing.T, filePath, data string) {
					assert.Equal(t, 3, strings.Count(data, `Evaluating model "symflower/symbolic-execution"`))
				},
			},
		})
	})

	// This case checks a beautiful bug where the Markdown export crashed when the current working directory contained a README.md file. While this is not the case during the tests (as the current work directory is the directory of this file), it certainly caused problems when our binary was executed from the repository root (which of course contained a README.md). Therefore, we sadly have to modify the current work directory right within the tests of this case to reproduce the problem and fix it forever.
	validate(t, &testCase{
		Name: "Current work directory contains a README.md",

		Before: func(t *testing.T, logger *log.Logger, resultPath string) {
			if err := os.Remove("README.md"); err != nil {
				if osutil.IsWindows() {
					require.Contains(t, err.Error(), "The system cannot find the file specified")
				} else {
					require.Contains(t, err.Error(), "no such file or directory")
				}
			}
			require.NoError(t, os.WriteFile("README.md", []byte(""), 0644))
		},
		After: func(t *testing.T, logger *log.Logger, resultPath string) {
			require.NoError(t, os.Remove("README.md"))
		},

		Arguments: []string{
			"--language", "golang",
			"--model", "symflower/symbolic-execution",
			"--repository", filepath.Join("golang", "plain"),
		},

		ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
			filepath.Join("result-directory", "categories.svg"):    nil,
			filepath.Join("result-directory", "evaluation.csv"):    nil,
			filepath.Join("result-directory", "evaluation.log"):    nil,
			filepath.Join("result-directory", "golang-summed.csv"): nil,
			filepath.Join("result-directory", "models-summed.csv"): nil,
			filepath.Join("result-directory", "README.md"):         nil,
			filepath.Join("result-directory", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): nil,
		},
	})
	validate(t, &testCase{
		Name: "Don't overwrite results path if it already exists",

		Before: func(t *testing.T, logger *log.Logger, resultPath string) {
			require.NoError(t, os.Mkdir(filepath.Join(resultPath, "result-directory"), 0600))
		},

		Arguments: []string{
			"--language", "golang",
			"--model", "symflower/symbolic-execution",
			"--repository", filepath.Join("golang", "plain"),
		},

		ExpectedResultFiles: map[string]func(t *testing.T, filePath string, data string){
			filepath.Join("result-directory-0", "categories.svg"):    nil,
			filepath.Join("result-directory-0", "evaluation.csv"):    nil,
			filepath.Join("result-directory-0", "evaluation.log"):    nil,
			filepath.Join("result-directory-0", "golang-summed.csv"): nil,
			filepath.Join("result-directory-0", "models-summed.csv"): nil,
			filepath.Join("result-directory-0", "README.md"):         nil,
			filepath.Join("result-directory-0", string(evaluatetask.IdentifierWriteTests), "symflower_symbolic-execution", "golang", "golang", "plain.log"): nil,
		},
	})
}

func TestEvaluateInitialize(t *testing.T) {
	type testCase struct {
		Name string

		Command *Evaluate

		ValidateCommand func(t *testing.T, command *Evaluate)
		ValidateContext func(t *testing.T, context *evaluate.Context)
		ValidateResults func(t *testing.T, resultsPath string)
	}

	validate := func(t *testing.T, tc *testCase) {
		t.Run(tc.Name, func(t *testing.T) {
			require.NotNil(t, tc.Command, "command must be non-nil")

			temporaryDirectory := t.TempDir()
			buffer, logger := log.Buffer()
			defer func() {
				if t.Failed() {
					t.Logf("Logs:\n%s", buffer.String())
				}
			}()

			tc.Command.logger = logger
			tc.Command.ResultPath = strings.ReplaceAll(tc.Command.ResultPath, "$TEMP_PATH", temporaryDirectory)

			var actualEvaluationContext *evaluate.Context
			assert.NotPanics(t, func() {
				c, cleanup := tc.Command.Initialize([]string{})
				cleanup()
				actualEvaluationContext = c
			})

			if tc.ValidateCommand != nil {
				tc.ValidateCommand(t, tc.Command)
			}
			if tc.ValidateContext != nil {
				require.NotNil(t, actualEvaluationContext)
				tc.ValidateContext(t, actualEvaluationContext)
			}
			if tc.ValidateResults != nil {
				tc.ValidateResults(t, temporaryDirectory)
			}
		})
	}

	// makeValidCommand is a helper to abstract all the default values that have to be set to make a command valid.
	makeValidCommand := func(modify func(command *Evaluate)) *Evaluate {
		c := &Evaluate{
			QueryAttempts:    1,
			Runs:             1,
			ExecutionTimeout: 1,
			TestdataPath:     filepath.Join("..", "..", "..", "testdata"),
			ResultPath:       filepath.Join("$TEMP_PATH", "result-directory"),
		}

		if modify != nil {
			modify(c)
		}

		return c
	}

	validate(t, &testCase{
		Name: "Custom result directory is created",

		Command: makeValidCommand(func(command *Evaluate) {
			command.ResultPath = filepath.Join("$TEMP_PATH", "some-directory")
		}),

		ValidateResults: func(t *testing.T, resultsPath string) {
			assert.DirExists(t, filepath.Join(resultsPath, "some-directory"))
		},
	})
	validate(t, &testCase{
		Name: "Selecting no language defaults to all",

		Command: makeValidCommand(func(command *Evaluate) {
			command.Languages = []string{}
		}),

		ValidateCommand: func(t *testing.T, command *Evaluate) {
			assert.Equal(t, []string{
				"golang",
				"java",
			}, command.Languages)
		},
		ValidateContext: func(t *testing.T, context *evaluate.Context) {
			assert.Equal(t, []language.Language{
				language.Languages["golang"],
				language.Languages["java"],
			}, context.Languages)
		},
	})
	validate(t, &testCase{
		Name: "Selecting no model defaults to all",

		Command: makeValidCommand(func(command *Evaluate) {
			command.Models = []string{}
		}),

		// Could also select arbitrary Ollama or new Openrouter models so sanity check that at least symflower is there.
		ValidateCommand: func(t *testing.T, command *Evaluate) {
			assert.Contains(t, command.Models, "symflower/symbolic-execution")
		},
		ValidateContext: func(t *testing.T, context *evaluate.Context) {
			modelIDs := make([]string, len(context.Models))
			for i, model := range context.Models {
				modelIDs[i] = model.ID()
			}
			assert.Contains(t, modelIDs, "symflower/symbolic-execution")
		},
	})
	validate(t, &testCase{
		Name: "Remove repository if language is not selected",

		Command: makeValidCommand(func(command *Evaluate) {
			command.Repositories = []string{
				filepath.Join("golang", "light"),
				filepath.Join("java", "light"),
			}
			command.Languages = []string{
				"golang",
			}
		}),

		ValidateCommand: func(t *testing.T, command *Evaluate) {
			assert.Equal(t, []string{
				filepath.Join("golang", "light"),
				filepath.Join("golang", "plain"),
			}, command.Repositories)
		},
		ValidateContext: func(t *testing.T, context *evaluate.Context) {
			assert.Equal(t, []string{
				filepath.Join("golang", "light"),
				filepath.Join("golang", "plain"),
			}, context.RepositoryPaths)
		},
	})
	validate(t, &testCase{
		Name: "Remove language if no repository is selected",

		Command: makeValidCommand(func(command *Evaluate) {
			command.Repositories = []string{
				filepath.Join("golang", "light"),
			}
			command.Languages = []string{
				"golang",
				"java",
			}
		}),

		ValidateCommand: func(t *testing.T, command *Evaluate) {
			assert.Equal(t, []string{
				"golang",
			}, command.Languages)
		},
		ValidateContext: func(t *testing.T, context *evaluate.Context) {
			assert.Equal(t, []language.Language{
				language.Languages["golang"],
			}, context.Languages)
		},
	})
	validate(t, &testCase{
		Name: "Selecting no repository defaults to all",

		Command: makeValidCommand(func(command *Evaluate) {
			command.Repositories = []string{}
		}),

		ValidateCommand: func(t *testing.T, command *Evaluate) {
			var repositoryPathsRelative []string
			for _, language := range language.Languages {
				directories, err := os.ReadDir(filepath.Join("..", "..", "..", "testdata", language.ID()))
				require.NoError(t, err)
				for _, directory := range directories {
					repositoryPathsRelative = append(repositoryPathsRelative, filepath.Join(language.ID(), directory.Name()))
				}
			}
			for _, repositoryPathRelative := range repositoryPathsRelative {
				assert.Contains(t, command.Repositories, repositoryPathRelative)
			}
		},
	})
}
