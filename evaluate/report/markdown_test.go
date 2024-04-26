package report

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zimmski/osutil/bytesutil"

	"github.com/symflower/eval-dev-quality/evaluate/metrics"
)

func TestMarkdownWriteToFile(t *testing.T) {
	type testCase struct {
		Name string

		Markdown Markdown

		ExpectedReport  string
		ExpectedSVGFile string
		ExpectedError   error
	}

	validate := func(t *testing.T, tc *testCase) {
		t.Run(tc.Name, func(t *testing.T) {
			temporaryDirectory := t.TempDir()
			markdownFilePath := filepath.Join(temporaryDirectory, "REPORT.md")

			actualError := tc.Markdown.WriteToFile(markdownFilePath)
			assert.Equal(t, tc.ExpectedError, actualError)
			actualReport, err := os.ReadFile(markdownFilePath)
			assert.NoError(t, err)

			assert.Equalf(t, bytesutil.StringTrimIndentations(tc.ExpectedReport), string(actualReport), "Full output:\n%s", actualReport)

			actualSVGContent, err := os.ReadFile(filepath.Join(temporaryDirectory, tc.Markdown.SVGPath))
			assert.NoError(t, err)
			expectedSVGContent, err := os.ReadFile(tc.ExpectedSVGFile)
			require.NoError(t, err)
			assert.Equal(t, string(expectedSVGContent), string(actualSVGContent))
		})
	}

	testTimeString := "2000-01-01 00:00:00"
	testTime, err := time.Parse(time.DateTime, testTimeString)
	require.NoError(t, err)

	validate(t, &testCase{
		Name: "No Models",

		Markdown: Markdown{
			DateTime: testTime,
			Version:  "1234",

			CSVPath: "./file.csv",
			LogPath: "./file.log",
			SVGPath: "./file.svg",
		},

		ExpectedReport: `
			# Evaluation from 2000-01-01 00:00:00

			![Bar chart that categorizes all evaluated models.](./file.svg)

			This report was generated by [DevQualityEval benchmark](https://github.com/symflower/eval-dev-quality) in ` + "`" + `version 1234` + "`" + `.

			## Results

			> Keep in mind that LLMs are nondeterministic. The following results just reflect a current snapshot.

			The results of all models have been divided into the following categories:
			- category unknown: Models in this category could not be categorized.
			- response error: Models in this category encountered an error.
			- response empty: Models in this category produced an empty response.
			- no code: Models in this category produced no code.
			- invalid code: Models in this category produced invalid code.
			- executable code: Models in this category produced executable code.
			- statement coverage reached: Models in this category produced code that reached full statement coverage.
			- no excess response: Models in this category did not respond with more content than requested.

			The following sections list all models with their categories. The complete log of the evaluation with all outputs can be found [here](./file.log). Detailed scoring can be found [here](./file.csv).

		`,
		ExpectedSVGFile: "testdata/empty.svg",
	})

	validate(t, &testCase{
		Name: "Simple Models",

		Markdown: Markdown{
			DateTime: testTime,
			Version:  "1234",

			CSVPath: "./file.csv",
			LogPath: "./file.log",
			SVGPath: "./file.svg",

			TotalScore: 1,
			AssessmentPerModel: map[string]metrics.Assessments{
				"ModelResponseError": metrics.NewAssessments(),
				"ModelNoCode": metrics.Assessments{
					metrics.AssessmentKeyResponseNoError:  1,
					metrics.AssessmentKeyResponseNotEmpty: 1,
				},
			},
		},

		ExpectedReport: `
			# Evaluation from 2000-01-01 00:00:00

			![Bar chart that categorizes all evaluated models.](./file.svg)

			This report was generated by [DevQualityEval benchmark](https://github.com/symflower/eval-dev-quality) in ` + "`" + `version 1234` + "`" + `.

			## Results

			> Keep in mind that LLMs are nondeterministic. The following results just reflect a current snapshot.

			The results of all models have been divided into the following categories:
			- category unknown: Models in this category could not be categorized.
			- response error: Models in this category encountered an error.
			- response empty: Models in this category produced an empty response.
			- no code: Models in this category produced no code.
			- invalid code: Models in this category produced invalid code.
			- executable code: Models in this category produced executable code.
			- statement coverage reached: Models in this category produced code that reached full statement coverage.
			- no excess response: Models in this category did not respond with more content than requested.

			The following sections list all models with their categories. The complete log of the evaluation with all outputs can be found [here](./file.log). Detailed scoring can be found [here](./file.csv).

			### Result category "response error"

			Models in this category encountered an error.

			- ` + "`ModelResponseError`" + `

			### Result category "no code"

			Models in this category produced no code.

			- ` + "`ModelNoCode`" + `

		`,
		ExpectedSVGFile: "testdata/two_models.svg",
	})
}

func TestBarChartModelsPerCategoriesSVG(t *testing.T) {
	type testCase struct {
		Name string

		Categories        []*metrics.AssessmentCategory
		ModelsPerCategory map[*metrics.AssessmentCategory]uint

		ExpectedFile  string
		ExpectedError error
	}

	validate := func(t *testing.T, tc *testCase) {
		t.Run(tc.Name, func(t *testing.T) {
			var actualSVGContent bytes.Buffer
			dummyModelsPerCategory := make(map[*metrics.AssessmentCategory][]string)
			for category, count := range tc.ModelsPerCategory {
				dummyModelsPerCategory[category] = make([]string, count)
			}

			actualError := barChartModelsPerCategoriesSVG(&actualSVGContent, tc.Categories, dummyModelsPerCategory)
			assert.Equal(t, tc.ExpectedError, actualError)

			expectedSVGContent, err := os.ReadFile(tc.ExpectedFile)
			require.NoError(t, err)
			assert.Equal(t, string(expectedSVGContent), actualSVGContent.String())
		})
	}

	validate(t, &testCase{
		Name: "Two Categories",

		Categories: []*metrics.AssessmentCategory{
			metrics.AssessmentCategoryResponseError,
			metrics.AssessmentCategoryResponseNoCode,
		},
		ModelsPerCategory: map[*metrics.AssessmentCategory]uint{
			metrics.AssessmentCategoryResponseError:  1,
			metrics.AssessmentCategoryResponseNoCode: 3,
		},

		ExpectedFile: "testdata/two_categories.svg",
	})

	validate(t, &testCase{
		Name: "All Categories",

		Categories: metrics.AllAssessmentCategories,
		ModelsPerCategory: map[*metrics.AssessmentCategory]uint{
			metrics.AssessmentCategoryResponseError:                1,
			metrics.AssessmentCategoryResponseEmpty:                2,
			metrics.AssessmentCategoryResponseNoCode:               3,
			metrics.AssessmentCategoryCodeInvalid:                  4,
			metrics.AssessmentCategoryCodeExecuted:                 5,
			metrics.AssessmentCategoryCodeCoverageStatementReached: 6,
			metrics.AssessmentCategoryCodeNoExcess:                 7,
		},

		ExpectedFile: "testdata/all_categories.svg",
	})
}
