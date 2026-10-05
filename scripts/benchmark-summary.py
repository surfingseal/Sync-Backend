import json,pathlib,math,statistics,re,sys
root=pathlib.Path(__file__).resolve().parents[1];out=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'benchmark-results/screening')
limits=json.loads((root/'examples/benchmark-thresholds.json').read_text())
def norm(s):return ' '.join(re.sub(r'[-_]', ' ',s.lower()).split())
def jaccard(a,b):
 a,b={norm(x) for x in a},{norm(x) for x in b};return len(a&b)/len(a|b) if a|b else 1
def percentile(a,q):
 if not a:return None
 a=sorted(a);p=(len(a)-1)*q;i=math.floor(p);return a[i]+(a[min(i+1,len(a)-1)]-a[i])*(p-i)
def compare(a,b):
 categorical=[norm(a['scene']['category'])==norm(b['scene']['category']),norm(a['scene']['time_of_day'])==norm(b['scene']['time_of_day']),norm(a['visual']['color_temperature'])==norm(b['visual']['color_temperature']),norm(a['music_profile']['tempo'])==norm(b['music_profile']['tempo'])]
 return {'categorical':sum(categorical)/len(categorical),'mood':jaccard(a['mood']['tags'],b['mood']['tags']),'genre':jaccard(a['music_profile']['genres'],b['music_profile']['genres']),'numeric':statistics.mean([abs(a['mood']['energy']-b['mood']['energy']),abs(a['mood']['valence']-b['mood']['valence']),abs(a['music_profile']['energy']-b['music_profile']['energy'])])}
def records(file):return [json.loads(line) for line in file.read_text().splitlines() if line.strip()]
files=list(out.glob('*.jsonl'));data={p.stem:records(p) for p in files}
baseline=data.get('baseline',[])
if not baseline:
 baseline=records(pathlib.Path(sys.argv[2]) if len(sys.argv)>2 else out/'baseline.jsonl')
reference={}
for r in sorted(baseline,key=lambda r:r['repetition']):
 if r['success'] and r['image_id'] not in reference:reference[r['image_id']]=r['analysis']
summary={}
for name,rows in data.items():
 success=[r for r in rows if r['success']];lat=[r['vertex']['vertex_total_ms'] for r in success];comparisons=[compare(reference[r['image_id']],r['analysis']) for r in success if r['image_id'] in reference]
 means={key:statistics.mean(x[key] for x in comparisons) if comparisons else None for key in ['categorical','mood','genre','numeric']}
 repeats=[]
 byImage={}
 for r in success:byImage.setdefault(r['image_id'],[]).append(r['analysis'])
 for image,analyses in byImage.items():
  for a in analyses[1:]:repeats.append(compare(analyses[0],a))
 s={'configuration':rows[0]['profile'],'runs':len(rows),'success_rate':len(success)/len(rows),'timeout_rate':sum(r.get('error_category')=='timeout' for r in rows)/len(rows),'failure_rate':1-len(success)/len(rows),'p50_ms':percentile(lat,.5),'p90_ms':percentile(lat,.9),'p95_ms':percentile(lat,.95),'max_success_ms':max(lat,default=None),'max_total_ms':max(r['total_request_ms'] for r in rows),'p95_total_including_censored_ms':percentile([r['total_request_ms'] for r in rows],.95),'retry_count':sum(max(0,r['vertex']['attempt_count']-1) for r in rows),'429_attempts':sum(r['vertex']['http_statuses'].count(429) for r in rows if r['vertex']['http_statuses']),'5xx_attempts':sum(sum(code>=500 for code in r['vertex']['http_statuses']) for r in rows if r['vertex']['http_statuses']),'invalid_response_count':sum(r.get('error_category')=='invalid_response' for r in rows),'comparison_pairs':len(comparisons),'reference_coverage':len(byImage.keys()&reference.keys()),'agreement':means,'repeat_consistency':{k:statistics.mean(x[k] for x in repeats) if repeats else None for k in means},'output_bytes_mean':statistics.mean(r['vertex']['output_bytes'] for r in success) if success else None,'output_tokens_mean':statistics.mean(r['vertex']['output_tokens'] for r in success) if success else None,'thought_tokens_mean':statistics.mean(r['vertex']['thought_tokens'] for r in success) if success else None}
 s['input_tokens_mean']=statistics.mean(r['vertex']['input_tokens'] for r in success) if success else None
 s['schema_success_given_completed_response']=1.0 if s['invalid_response_count']==0 else None
 s['valid_analysis_per_request']=s['success_rate']
 s['quality_accepted']=bool(s['success_rate']>=limits['schema_success_min'] and len(comparisons)>=.9*len(rows) and means['categorical']>=limits['categorical_agreement_min'] and means['mood']>=limits['mood_jaccard_min'] and means['genre']>=limits['genre_jaccard_min'] and means['numeric']<=limits['numeric_abs_diff_max'])
 s['latency_target_met']=bool(s['timeout_rate']<=limits['timeout_rate_max'] and s['p50_ms'] is not None and s['p50_ms']<=limits['p50_vertex_ms_max'] and s['p95_ms']<=limits['p95_vertex_ms_max'])
 summary[name]=s
(out/'summary.json').write_text(json.dumps({'thresholds':limits,'profiles':summary,'notes':['Latency percentiles use successful calls; timeout observations are right-censored and reported separately.','Baseline is a reference, not ground truth. Missing references are excluded and coverage is explicit.','24 samples provide screening estimates, not definitive p95.','retry_wait_ms is inter-attempt gap estimation; unary call includes auth/network/model time.']},indent=2))
for name,s in summary.items():print(name,'runs=',s['runs'],'success=',round(s['success_rate'],3),'p50/p95=',tuple(round(s[k]/1000,2) if s[k] is not None else None for k in ['p50_ms','p95_ms']),'agreement=',s['agreement'],'quality=',s['quality_accepted'])
