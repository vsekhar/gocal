package itercal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/vsekhar/gocal/internal/batch"
	"github.com/vsekhar/gocal/internal/cache"
	"gonum.org/v1/gonum/stat"
	directory "google.golang.org/api/admin/directory/v1"
)

const maxAge = 7 * 24 * time.Hour

func loadIndex(dir string) (bleve.Index, error) { return bleve.Open(dir) }

func Buildings(ctx context.Context, cacheSpace *cache.Space, srv *directory.Service) (bleve.Index, error) {
	return cache.GetOrCreate(cacheSpace, "buildings", maxAge, loadIndex, func(dir string) (bleve.Index, error) {
		// Fetch all and save index
		idx, err := bleve.New(dir, bleve.NewIndexMapping())
		if err != nil {
			return nil, err
		}

		buildings := make(chan *directory.Building, 10000)
		batches := make(chan []*directory.Building)

		wg := sync.WaitGroup{}
		wg.Add(2)

		// Producer
		go func() {
			defer wg.Done()
			defer close(buildings)
			err = ForEachBuilding(ctx, srv, func(b *directory.Building) error {
				buildings <- b
				return nil
			})
			if err != nil {
				log.Fatal(err)
			}
		}()

		// Consumer
		go func() {
			defer wg.Done()
			for bs := range batches {
				batch := idx.NewBatch()
				for _, b := range bs {
					batch.Index(b.BuildingId, b)
				}
				if err := idx.Batch(batch); err != nil {
					log.Fatal(err)
				}
			}
		}()

		batch.Up(buildings, batches)
		close(batches)
		wg.Wait()

		return idx, err
	})
}

type Resources []*directory.CalendarResource

func ResourcesInBuilding(ctx context.Context, cacheSpace *cache.Space, srv *directory.Service, buildingId string) (Resources, error) {
	const resourcesFilename = "resources.json"

	loadResources := func(dir string) (Resources, error) {
		f, err := os.Open(filepath.Join(dir, resourcesFilename))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		var ret Resources
		if err := dec.Decode(&ret); err != nil {
			return nil, err
		}
		return ret, nil
	}

	createResources := func(dir string) (Resources, error) {
		var ret Resources
		err := ForEachResourceInBuilding(ctx, srv, buildingId, func(r *directory.CalendarResource) error {
			ret = append(ret, r)
			return nil
		})
		if err != nil {
			return nil, err
		}
		f, err := os.Create(filepath.Join(dir, resourcesFilename))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		if err = enc.Encode(ret); err != nil {
			return nil, err
		}
		return ret, nil
	}

	return cache.GetOrCreate(cacheSpace, buildingId, maxAge, loadResources, createResources)
}

func confidenceInFirst(f []float64) bool {
	const minStdScore = 2.0 // standard deviations away from the mean

	if len(f) == 0 {
		panic("empty values")
	}
	if len(f) == 1 {
		return true
	}

	mean, stdev := stat.MeanStdDev(f, nil)
	score := stat.StdScore(f[0], mean, stdev)
	return score > minStdScore
}

func SearchBuildings(idx bleve.Index, q string) (buildingID string, err error) {
	query := bleve.NewQueryStringQuery(q)
	sr := bleve.NewSearchRequestOptions(query, 50, 0, false)
	results, err := idx.Search(sr)
	if err != nil {
		return "", err
	}
	scores := make([]float64, results.Total)
	for i, d := range results.Hits {
		scores[i] = d.Score
	}
	if confidenceInFirst(scores) {
		return results.Hits[0].ID, nil
	}

	for _, d := range results.Hits {
		log.Printf("%s: %f", d.ID, d.Score)
	}
	return "", fmt.Errorf("%d buildings found", results.Total)
}
