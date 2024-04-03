package cmd

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zimmski/osutil"
	"golang.org/x/exp/maps"

	"github.com/symflower/eval-symflower-codegen-testing/evaluate"
	"github.com/symflower/eval-symflower-codegen-testing/language"
	"github.com/symflower/eval-symflower-codegen-testing/model"
	"github.com/symflower/eval-symflower-codegen-testing/provider"
	_ "github.com/symflower/eval-symflower-codegen-testing/provider/openrouter"
	_ "github.com/symflower/eval-symflower-codegen-testing/provider/symflower"
)

// Evaluate holds the "evaluation" command.
type Evaluate struct {
	// Languages determines which language should be used for the evaluation, or empty if all languages should be used.
	Languages []string `long:"language" description:"Evaluate with this language. By default all languages are used."`
	// Models determines which models should be used for the evaluation, or empty if all models should be used.
	Models []string `long:"model" description:"Evaluate with this model. By default all models are used."`
	// Repositories determines which repository should be used for the evaluation, or empty if all repositories should be used.
	Repositories []string `long:"repository" description:"Evaluate with this repository. By default all repositories are used."`
	// TestdataPath determines the testdata path where all repositories reside grouped by languages.
	TestdataPath string `long:"testdata" description:"Path to the testdata directory where all repositories reside grouped by languages." default:"testdata/"`

	// ProviderTokens holds all API tokens for the providers.
	ProviderTokens map[string]string `long:"tokens" description:"API tokens for model providers (of the form '$provider:$token,...')." env:"PROVIDER_TOKEN"`
}

func (command *Evaluate) Execute(args []string) (err error) {
	// Gather languages.
	if len(command.Languages) == 0 {
		command.Languages = maps.Keys(language.Languages)
	} else {
		for _, languageID := range command.Languages {
			if _, ok := language.Languages[languageID]; !ok {
				ls := maps.Keys(language.Languages)
				sort.Strings(ls)

				log.Fatalf("ERROR: language %s does not exist. Valid languages are: %s", languageID, strings.Join(ls, ", "))
			}
		}
	}
	sort.Strings(command.Languages)

	commandRepositories := map[string]bool{}
	for _, r := range command.Repositories {
		commandRepositories[r] = true
	}

	// Gather models.
	models := map[string]model.Model{}
	for _, p := range provider.Providers {
		for _, m := range p.Models() {
			if t, ok := p.(provider.InjectToken); ok {
				token, ok := command.ProviderTokens[p.ID()]
				if !ok {
					log.Fatalf("ERROR: model provider %q requires an API token but none was given. Specify one using command line arguments or environment variables.", p.ID())
				}
				t.SetToken(token)
			}

			models[m.ID()] = m
		}
	}
	modelIDs := maps.Keys(models)
	sort.Strings(modelIDs)
	if len(command.Models) == 0 {
		command.Models = modelIDs
	} else {
		for _, modelID := range command.Models {
			if _, ok := models[modelID]; !ok {
				log.Fatalf("ERROR: model %s does not exist. Valid models are: %s", modelID, strings.Join(modelIDs, ", "))
			}
		}
	}
	sort.Strings(command.Models)

	if err := osutil.DirExists(command.TestdataPath); err != nil {
		log.Fatalf("ERROR: testdata path %q cannot be accessed: %s", command.TestdataPath, err)
	}
	command.TestdataPath, err = filepath.Abs(command.TestdataPath)
	if err != nil {
		log.Fatalf("ERROR: could not resolve testdata path %q to an absolute path: %s", command.TestdataPath, err)
	}

	// Check that models and languages can be evaluated by executing the "plain" repositories.
	log.Printf("Checking that models and languages can be used for evaluation")
	excludeModelIDs := map[string]bool{}
	for _, languageID := range command.Languages {
		for _, modelID := range command.Models {
			model := models[modelID]
			language := language.Languages[languageID]

			if err := evaluate.EvaluateRepository(model, language, filepath.Join(command.TestdataPath, language.ID(), "plain")); err != nil {
				log.Printf("Excluding model %q since it was not able to solve the \"plain\" repository for language %q: %+v", modelID, languageID, err)
				excludeModelIDs[modelID] = true
			}
		}
	}

	// Evaluating models and languages.
	log.Printf("Evaluating models and languages")
	for _, languageID := range command.Languages {
		languagePath := filepath.Join(command.TestdataPath, languageID)

		repositories, err := os.ReadDir(languagePath)
		if err != nil {
			log.Fatalf("ERROR: language path %q cannot be accessed: %s", languagePath, err)
		}

		for _, repository := range repositories {
			if !repository.IsDir() || (len(commandRepositories) > 0 && !commandRepositories[repository.Name()]) {
				continue
			}

			for _, modelID := range command.Models {
				if excludeModelIDs[modelID] {
					continue
				}

				model := models[modelID]
				language := language.Languages[languageID]

				if err := evaluate.EvaluateRepository(model, language, filepath.Join(languagePath, repository.Name())); err != nil {
					log.Printf("ERROR: Model %q encountered a hard error for language %q, repository %q: %+v", modelID, languageID, repository.Name(), err)
				}
			}
		}
	}

	return nil
}
