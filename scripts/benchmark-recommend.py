#!/usr/bin/env python3
"""Explicit paid integration: actual POST /recommend and independent YouTube read.
Uses saved real analyses, never images/tokens. Default unit tests never run it.
"""
import argparse,collections,json,os,pathlib,random,statistics,time,urllib.request,urllib.parse
p=argparse.ArgumentParser();p.add_argument('--results',required=True);p.add_argument('--out',required=True);p.add_argument('--profiles',default='baseline,low-1920,low-1280,lite-1280');p.add_argument('--images',default='sunset,cafe,city-night');p.add_argument('--base',default='http://localhost:8080');a=p.parse_args()
out=pathlib.Path(a.out);out.mkdir(parents=True,exist_ok=True);results=[]
for profile in a.profiles.split(','):
 path=pathlib.Path(a.results)/(profile+'.jsonl')
 records=[json.loads(line) for line in path.read_text().splitlines()]
 for image in a.images.split(','):
  record=next((r for r in records if r['image_id']==image and r['success']),None)
  if record is None: print('SKIP: no successful analysis',profile,image,flush=True);continue
  destination=out/(profile+'-'+image+'.json')
  if destination.exists():results.append(json.loads(destination.read_text()));continue
  request={'analysis':record['analysis'],'preferences':{'languages':['ko','en'],'count':10}}
  row={'profile':profile,'image_id':image,'analysis':record['analysis'],'analysis_total_ms':record['total_request_ms']}
  start=time.perf_counter()
  try:
   req=urllib.request.Request(a.base+'/api/v1/recommend',data=json.dumps(request).encode(),headers={'Content-Type':'application/json'},method='POST')
   with urllib.request.urlopen(req,timeout=25) as reply:response=json.load(reply);row['http_status']=reply.status
   row['recommendation_ms']=(time.perf_counter()-start)*1000;row['time_to_recommendations_ms']=row['analysis_total_ms']+row['recommendation_ms'];row['response']=response
   ids=[t['video_id'] for t in response['tracks']];row['verified_metadata']=[]
   if ids:
    params=urllib.parse.urlencode({'key':os.environ['YOUTUBE_API_KEY'],'id':','.join(ids),'part':'snippet,contentDetails,status,statistics'})
    with urllib.request.urlopen('https://www.googleapis.com/youtube/v3/videos?'+params,timeout=15) as reply:details=json.load(reply)
    actual={v['id']:v for v in details.get('items',[])};assert all(i in actual for i in ids)
    row['verified_metadata']=details['items'];channels=collections.Counter(v['snippet']['channelId'] for v in details['items'])
    row['quality']={'returned_count':len(ids),'unique_channels':len(channels),'max_channel_repeat':max(channels.values(),default=0),'licensed_count':sum(v['contentDetails'].get('licensedContent',False) for v in details['items']),'median_views':statistics.median(int(v.get('statistics',{}).get('viewCount',0)) for v in details['items'])}
   row['success']=True
  except Exception as e:
   # Never print HTTPError URLs/bodies (public metadata URL contains the key).
   row['success']=False;row['error_category']=type(e).__name__;row['recommendation_ms']=(time.perf_counter()-start)*1000
  destination.write_text(json.dumps(row,ensure_ascii=False,indent=2));results.append(row)
  print('recommendation',profile,image,'success=',row['success'],'ms=',round(row['recommendation_ms']),flush=True)
(out/'summary.json').write_text(json.dumps(results,ensure_ascii=False,indent=2))
# Blind review has no configuration/model labels and no fabricated human scores.
blind=[]
for image in a.images.split(','):
 group=[r for r in results if r['image_id']==image and r['success']];random.Random(812).shuffle(group)
 for i,row in enumerate(group):blind.append({'review_id':image+'-'+chr(65+i),'image_id':image,'tracks':row['response']['tracks'],'human_mood_fit_1_to_5':None})
(out/'blind-review.json').write_text(json.dumps(blind,ensure_ascii=False,indent=2))
# Keep the mapping separate from the blind file.
key=[]
for image in a.images.split(','):
 group=[r for r in results if r['image_id']==image and r['success']];random.Random(812).shuffle(group)
 key.extend({'review_id':image+'-'+chr(65+i),'profile':r['profile']} for i,r in enumerate(group))
(out/'blind-review-key.json').write_text(json.dumps(key,indent=2))
