#!/usr/bin/env python3
"""Pack saved cmd/benchmark JSONL for offline consistency analysis; no API calls."""
import argparse, json, pathlib, collections
p=argparse.ArgumentParser();p.add_argument('input');p.add_argument('output');args=p.parse_args()
groups=collections.defaultdict(list)
for line in pathlib.Path(args.input).read_text().splitlines():
    r=json.loads(line)
    if r.get('success') and r.get('analysis'):
        groups[r['image_id']].append((r['repetition'],r['analysis']))
results=[]
for name,rows in sorted(groups.items()):
    rows.sort(key=lambda x:x[0])
    if len(rows) not in (2,3):
        raise SystemExit(f'{name}: 2 or 3 successful repeats are required')
    results.append({'photo_id':name,'analyses':[a for _,a in rows]})
path=pathlib.Path(args.output)
if path.exists():raise SystemExit('Output already exists; use a new filename')
path.write_text(json.dumps(results,indent=2)+'\n')
