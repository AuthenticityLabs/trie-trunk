// Replace disposable v0 fixtures after changing the PIL Definition wire format.
// Build rust/ in release mode first so the Go cache tests can link its library.
import {execFileSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {mkdirSync} from 'node:fs';
const root = new URL('../', import.meta.url);
const fixture = name => fileURLToPath(new URL(`client/services/fixtures/${name}`, root));
const pac = fileURLToPath(new URL('../pill-packer/rust/tests/fixtures/pac', root));
const env = {...process.env,
  PIL_NATIVE_FIXTURE_DIR:fixture('native-pil'),
  PIL_NATIVE_RANGE_FIXTURE_DIR:fixture('native-ranges'),
  PIL_VIEW_FIXTURE_DIR:fixture('pac-view'),
  PIL_REFERENCE_FIXTURE_DIR:fixture('references'),
  PIL_PAC_FIXTURE_DIR:pac,
  PIL_STATE_FIXTURE_DIR:fixture('state-pages'),
  PIL_HISTORY_FIXTURE_DIR:fixture('replay'),
  PIL_LIVE_VIEW_FIXTURE_DIR:fixture('pac-live'),
  PIL_BUCKET_FIXTURE_DIR:fixture('pac-buckets'),
  PIL_REJECTION_FIXTURE_DIR:fixture('rejections'),
  PIL_RANGE_FIXTURE:fixture('native-ranges/multi-hour.pil'),
  TRUNK_SUMMARY_FIXTURE:fixture('daily-summary'),
  UPDATE_PAC_STORAGE_FIXTURE:'1',
  UPDATE_TIME_TRIE_FIXTURE:'1',
  PIL_TRUNK_FIXTURE_DIR:fixture('trunk'),
};
for(const name of ['native-pil','native-ranges','pac-view','references','state-pages','replay','pac-live','pac-buckets','rejections','pac-storage'])mkdirSync(fixture(name),{recursive:true});
const tests = [
  'NativeHTTPInterop','NativeCollectHTTPGapOnlyAndLazyPayload',
  'PagedViewNegotiationSnapshotAndCache','ReferenceProjectionExplicitBoundsAndSparseValues',
  'PACGzipReuseAndFixture','PACNativePlacementAndRows','NativeStatePagesHTTP',
  'ReplayHTTPMinorHistoryAndInvalidation','LiveViewPaginationSurvivesFinalization',
  'ViewBucketCoverageIsFullScopedAndSealed','RejectionInspectionScopeCompletionAndExactIntegers',
  'PACRangeStreamingDoesNotInspectInteriorPacks',
  'StoredHourlyPACRecoveryAndDelivery','HourlyPACWaitsForClosingGrace','PACCrossPageGapFixtures',
  'SummaryNumericWidthsAndBases',
  'DailySummaryHTTPFixture',
  'PositionalTimeTrieNullZeroAndHourlySegments',
  'TrunkBootstrapIncludesEveryDefinitionAndActualFolderPaths',
];
execFileSync('go',['test','./server/cache','-count=1','-run',`^Test(${tests.join('|')})$`],{cwd:fileURLToPath(new URL('go/',root)),env,stdio:'inherit'});
