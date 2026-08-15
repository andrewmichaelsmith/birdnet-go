package imageprovider_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tphakala/birdnet-go/internal/conf/conftest"
	"github.com/tphakala/birdnet-go/internal/imageprovider"
)

// mockBoundedProvider is a primary provider whose dataset is fixed and known, so
// it can report coverage without a network call -- the AviCommons shape. Fetch
// always fails, which is what a real bounded provider does for a name outside
// its dataset.
type mockBoundedProvider struct {
	mockImageProvider
	covered map[string]bool
}

func (m *mockBoundedProvider) Covers(scientificName string) bool {
	return m.covered[scientificName]
}

// speciesOutsideBirdDataset are species BirdNET-Go detects with its bat and
// multi-taxa models. None can appear in a bird-only snapshot, so the primary
// provider will never serve them however many times it is asked.
var speciesOutsideBirdDataset = []string{
	"Tadarida brasiliensis", // Mexican free-tailed bat
	"Myotis lucifugus",      // Little brown bat
	"Eptesicus fuscus",      // Big brown bat
	"Lasiurus borealis",     // Eastern red bat
	"Lasiurus intermedius",  // Northern yellow bat
}

// TestBoundedProviderMissConsultsFallbackUnderPolicyNone covers the reported
// symptom: a bat detection shows a placeholder because the bird-only primary
// cannot serve it and the default policy stops anything else being asked.
func TestBoundedProviderMissConsultsFallbackUnderPolicyNone(t *testing.T) {
	for _, species := range speciesOutsideBirdDataset {
		t.Run(species, func(t *testing.T) {
			// Not parallel: mutates the process-global settings snapshot.
			settings := conftest.GetTestSettings()
			settings.Realtime.Dashboard.Thumbnails.ImageProvider = providerAvicommons
			settings.Realtime.Dashboard.Thumbnails.FallbackPolicy = "none"
			applyGlobalSettings(t, settings)

			store := newMockStore()

			primaryProvider := &mockBoundedProvider{
				mockImageProvider: mockImageProvider{shouldFail: true},
				covered:           map[string]bool{}, // covers no bats
			}
			primaryCache := imageprovider.InitCache(providerAvicommons, primaryProvider, nil, store)
			defer func() { assert.NoError(t, primaryCache.Close()) }()

			fallbackProvider := &mockImageProvider{}
			fallbackCache := imageprovider.InitCache(providerWikimedia, fallbackProvider, nil, store)
			defer func() { assert.NoError(t, fallbackCache.Close()) }()

			registry := imageprovider.NewImageProviderRegistry()
			require.NoError(t, registry.Register(providerAvicommons, primaryCache))
			require.NoError(t, registry.Register(providerWikimedia, fallbackCache))
			primaryCache.SetRegistry(registry)

			img, err := primaryCache.Get(species)

			require.NoError(t, err,
				"a species the bounded primary cannot cover must fall through to a provider that can")
			assert.Equal(t, 1, fallbackProvider.getFetchCount(),
				"fallback must be consulted for a species outside the bounded dataset")
			assert.NotEmpty(t, img.URL, "fallback should have produced an image URL")
		})
	}
}

// TestBoundedProviderHitStillHonoursPolicyNone pins the other half of the rule.
// The exemption is only for species the dataset structurally lacks; a covered
// species that merely failed this once is a transient error, and "none" must
// still suppress the fallback there. Without this the change would quietly
// become "fallbackpolicy: all" for everyone.
func TestBoundedProviderHitStillHonoursPolicyNone(t *testing.T) {
	// Not parallel: mutates the process-global settings snapshot.
	settings := conftest.GetTestSettings()
	settings.Realtime.Dashboard.Thumbnails.ImageProvider = providerAvicommons
	settings.Realtime.Dashboard.Thumbnails.FallbackPolicy = "none"
	applyGlobalSettings(t, settings)

	store := newMockStore()

	const coveredBird = "Parus major"
	primaryProvider := &mockBoundedProvider{
		mockImageProvider: mockImageProvider{shouldFail: true}, // transient failure
		covered:           map[string]bool{coveredBird: true},
	}
	primaryCache := imageprovider.InitCache(providerAvicommons, primaryProvider, nil, store)
	defer func() { assert.NoError(t, primaryCache.Close()) }()

	fallbackProvider := &mockImageProvider{}
	fallbackCache := imageprovider.InitCache(providerWikimedia, fallbackProvider, nil, store)
	defer func() { assert.NoError(t, fallbackCache.Close()) }()

	registry := imageprovider.NewImageProviderRegistry()
	require.NoError(t, registry.Register(providerAvicommons, primaryCache))
	require.NoError(t, registry.Register(providerWikimedia, fallbackCache))
	primaryCache.SetRegistry(registry)

	_, err := primaryCache.Get(coveredBird)

	require.Error(t, err,
		"a covered species that failed transiently must not fall through under policy 'none'")
	assert.Equal(t, 0, fallbackProvider.getFetchCount(),
		"fallback must NOT be consulted for a species the bounded dataset covers")
}

// TestAviCommonsCoversAgreesWithFetch guards the invariant the exemption rests
// on, against the real shipped snapshot: Covers must report exactly the names
// Fetch can serve. If Covers ever over-reported, it would suppress the fallback
// for a species the provider then fails to supply -- reintroducing the
// placeholder this change removes.
func TestAviCommonsCoversAgreesWithFetch(t *testing.T) {
	t.Parallel()

	provider, err := imageprovider.NewAviCommonsProvider(os.DirFS("."), false)
	require.NoError(t, err, "the shipped snapshot should load")

	names := append([]string{
		"Parus major",      // in the snapshot
		"Turdus merula",    // in the snapshot
		"Struthio camelus", // in the snapshot
	}, speciesOutsideBirdDataset...)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			covers := provider.Covers(name)
			_, fetchErr := provider.Fetch(name)
			assert.Equal(t, covers, fetchErr == nil,
				"Covers(%q)=%v must match whether Fetch succeeds", name, covers)
		})
	}
}

// TestAviCommonsDoesNotCoverBats states the fact the whole change rests on, so
// that a future snapshot which does gain bat coverage fails here loudly rather
// than leaving a now-pointless exemption in place.
func TestAviCommonsDoesNotCoverBats(t *testing.T) {
	t.Parallel()

	provider, err := imageprovider.NewAviCommonsProvider(os.DirFS("."), false)
	require.NoError(t, err, "the shipped snapshot should load")

	for _, species := range speciesOutsideBirdDataset {
		assert.False(t, provider.Covers(species),
			"the AviCommons snapshot is bird-only; %s should not be in it", species)
	}
}
