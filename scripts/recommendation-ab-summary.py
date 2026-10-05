#!/usr/bin/env python3
"""Summarize explicit read-only A/B results; no API calls or invented ratings."""
import collections,json,pathlib,statistics,sys,hashlib
root=pathlib.Path(sys.argv[1]);rows=[json.loads(p.read_text()) for p in sorted((root/'query-ab').glob('*.json'))]
def median(values):return statistics.median(values) if values else None
def metrics(ranked):
 views=[r['Video']['ViewCount'] for r in ranked];likes=[r['Video']['LikeCount'] for r in ranked if r['Video']['LikeCount'] is not None]
 channels=collections.Counter(r['Video']['ChannelID'] or r['Video']['ChannelTitle'] for r in ranked)
 return {'returned_count':len(ranked),'median_views':median(views),'min_views':min(views,default=None),'max_views':max(views,default=None),'median_likes':median(likes),'min_likes':min(likes,default=None),'known_likes_count':len(likes),'licensed_count':sum(r['Video']['LicensedContent'] for r in ranked),'unique_channels':len(channels),'max_channel_repeat':max(channels.values(),default=0),'discovery_count':sum(r['Video']['ViewCount']<10000 for r in ranked),'established_count':sum(r['Video']['ViewCount']>=100000 for r in ranked),'median_popularity':median([r['Scores']['Popularity'] for r in ranked]),'mean_mood':statistics.mean([r['Scores']['Mood'] for r in ranked]) if ranked else None}
summary=[];old=[];new=[];key={};blind=['# Query A/B blind review','', '모델/검색어 방식을 숨겼습니다. 실제 곡을 듣고 각 X/Y 그룹에 분위기 적합도·친숙도/신뢰감·듣고 싶은 정도를 1–5점으로 기록하세요. 사람 점수는 미평가 상태입니다. 동일 채널은 동일 아티스트라는 보장이 없습니다.',''];visible=['# Query A/B results (new ranker)','']
by={}
for r in rows:
 if not r['Success']:continue
 trace=r['Trace'];videos=trace['Videos'] or [];viewcounts=[v['ViewCount'] for v in videos];likecounts=[v['LikeCount'] for v in videos if v['LikeCount'] is not None]
 one={'image_id':r['ImageID'],'mode':r['Mode'],'query_generation_ms':trace['QueryMS'],'query_fallback':trace['QueryFallback'],'generated_queries':trace['GeneratedQueries'],'search_queries':trace['SearchQueries'],'search_ms':trace['SearchMS'],'metadata_ms':trace['MetadataMS'],'recommendation_total_ms':trace['TotalMS'],'ranking_ms':trace['RankingMS'],'new_ranking_ms':r['NewRankingMS'],'raw_count':trace['RawCount'],'detailed_count':len(videos),'eligible_count':len(trace['Candidates'] or []),'AI_transform_filtered':trace['AITransformFiltered'],'all_eligibility_filtered':trace['EligibilityFiltered'],'raw_view_distribution':{'min':min(viewcounts,default=None),'median':median(viewcounts),'max':max(viewcounts,default=None)},'raw_like_distribution':{'min':min(likecounts,default=None),'median':median(likecounts),'max':max(likecounts,default=None)},'raw_licensed_fraction':sum(v['LicensedContent'] for v in videos)/len(videos) if videos else None,'old':metrics(r['OldRanking'] or []),'new':metrics(r['NewRanking'] or [])}
 summary.append(one);old.append({'image_id':r['ImageID'],'mode':r['Mode'],'tracks':r['OldRanking']});new.append({'image_id':r['ImageID'],'mode':r['Mode'],'tracks':r['NewRanking']});by.setdefault(r['ImageID'],{})[r['Mode']]=r
for image,pair in by.items():
 visible.extend(['## '+image,''])
 blind.extend(['## '+image,''])
 flip=hashlib.sha256(image.encode()).digest()[0]%2;methods=['gemini','deterministic'] if flip else ['deterministic','gemini']
 for label,mode in zip(['X','Y'],methods):
  if mode not in pair:continue
  key[image+'-'+label]=mode;r=pair[mode]
  blind.extend(['### '+label,'','분위기 적합도: ____ /5 · 친숙도/신뢰감: ____ /5 · 듣고 싶은 정도: ____ /5',''])
  visible.extend(['### '+mode,'','Queries: '+json.dumps(r['Trace']['SearchQueries'],ensure_ascii=False),'','| Rank | Title / Channel | Views | Likes | Licensed | Mood | Popularity | Trust | Final | Discovery |','|---:|---|---:|---:|---|---:|---:|---:|---:|---|'])
  for index,t in enumerate(r['NewRanking'] or [],1):
   v=t['Video'];s=t['Scores'];title=v['Title'].replace('|','/').replace('\n',' ');url='https://www.youtube.com/watch?v='+v['VideoID']
   blind.append(f"{index}. [{title.replace('[','').replace(']','')}]({url}) — {v['ChannelTitle']}")
   visible.append(f"| {index} | [{title.replace('[','').replace(']','')}]({url}) / {v['ChannelTitle'].replace('|','/')} | {v['ViewCount']} | {v['LikeCount'] if v['LikeCount'] is not None else 'unknown'} | {v['LicensedContent']} | {s['Mood']:.3f} | {s['Popularity']:.3f} | {s['Trust']:.3f} | {s['Final']:.4f} | {v['ViewCount']<10000} |")
  if not r['NewRanking']:blind.append('추천곡 없음.')
  blind.append('');visible.append('')
for name,data in [('summary.json',summary),('old-ranking.json',old),('new-ranking.json',new),('gemini-query.json',[s for s in summary if s['mode']=='gemini']),('deterministic-query.json',[s for s in summary if s['mode']=='deterministic']),('query-ab-review-key.json',key)]: (root/name).write_text(json.dumps(data,ensure_ascii=False,indent=2))
(root/'query-ab-review.md').write_text('\n'.join(blind)+'\n');(root/'query-ab-results.md').write_text('\n'.join(visible)+'\n')
print('summarized',len(summary),'successful query/search requests for',len(by),'distinct saved analyses')
for mode in ['gemini','deterministic']:
 group=[s for s in summary if s['mode']==mode]
 if not group:continue
 print(mode,'n',len(group),'query_p50_ms',median([s['query_generation_ms'] for s in group]),'total_p50_ms',median([s['recommendation_total_ms'] for s in group]),'old/new counts',sum(s['old']['returned_count'] for s in group),sum(s['new']['returned_count'] for s in group),'old/new median-of-medians views',median([s['old']['median_views'] for s in group if s['old']['median_views'] is not None]),median([s['new']['median_views'] for s in group if s['new']['median_views'] is not None]))
