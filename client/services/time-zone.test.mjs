import {test} from 'node:test';import assert from 'node:assert/strict';
import {zonedTimestamp,parseZonedTimestamp,timeZoneOffset} from './time-zone.ts';
import {timeWindow,dayCoverage} from './calendar.ts';
test('profile offsets use the timestamp including DST and fractional-hour zones',()=>{
 assert.equal(timeZoneOffset(Date.parse('2026-01-01T00:00:00Z'),'America/New_York'),-300);
 assert.equal(timeZoneOffset(Date.parse('2026-07-01T00:00:00Z'),'America/New_York'),-240);
 assert.equal(timeZoneOffset(Date.parse('2026-07-01T00:00:00Z'),'Asia/Kathmandu'),345);
 const time=Date.parse('2026-10-29T12:34:56.789Z');assert.equal(parseZonedTimestamp(zonedTimestamp(time,'America/New_York'),'America/New_York'),time);
});
test('ambiguous and nonexistent wall times do not silently shift the search window',()=>{
 assert.throws(()=>parseZonedTimestamp('2026-03-08T02:30:00','America/New_York'),/does not exist/);
 assert.throws(()=>parseZonedTimestamp('2026-11-01T01:30:00','America/New_York'),/occurs twice/);
 const w=timeWindow(['2026-03-08','2026-03-08'],'00:00:00','23:59:59.999',Date.parse('2027-01-01Z'),'America/New_York');assert.equal(Date.parse(w.to+'Z')-Date.parse(w.from+'Z'),23*3600000);
 assert.equal(dayCoverage('2026-03-08',[{from:Date.parse(w.from+'Z'),to:Date.parse(w.to+'Z')}],'America/New_York'),'full');
});
