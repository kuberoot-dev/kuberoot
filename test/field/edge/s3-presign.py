#!/usr/bin/env python3
"""Make a private AWS SigV4 GET URL; never print keys or signed query strings."""
import argparse,datetime,hashlib,hmac,json,pathlib,urllib.parse

def presign(endpoint,bucket,key,credentials,expires=3600,now=None):
 now=now or datetime.datetime.now(datetime.timezone.utc)
 parsed=urllib.parse.urlsplit(endpoint);assert parsed.scheme in ['http','https'] and not parsed.query and not parsed.fragment
 date=now.strftime('%Y%m%d');stamp=now.strftime('%Y%m%dT%H%M%SZ');region=credentials.get('region','us-east-1');scope=f'{date}/{region}/s3/aws4_request'
 path=urllib.parse.quote(parsed.path.rstrip('/')+'/'+bucket+'/'+key,safe='/~')
 query={'X-Amz-Algorithm':'AWS4-HMAC-SHA256','X-Amz-Credential':credentials['access_key']+'/'+scope,'X-Amz-Date':stamp,'X-Amz-Expires':str(expires),'X-Amz-SignedHeaders':'host'}
 canonical_query=urllib.parse.urlencode(sorted(query.items()),quote_via=urllib.parse.quote,safe='~')
 request='GET\n'+path+'\n'+canonical_query+'\nhost:'+parsed.netloc+'\n\nhost\nUNSIGNED-PAYLOAD'
 message='AWS4-HMAC-SHA256\n'+stamp+'\n'+scope+'\n'+hashlib.sha256(request.encode()).hexdigest()
 signing=('AWS4'+credentials['secret_key']).encode()
 for part in [date,region,'s3','aws4_request']:signing=hmac.new(signing,part.encode(),hashlib.sha256).digest()
 signature=hmac.new(signing,message.encode(),hashlib.sha256).hexdigest()
 return urllib.parse.urlunsplit((parsed.scheme,parsed.netloc,path,canonical_query+'&X-Amz-Signature='+signature,''))
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--endpoint',required=True);p.add_argument('--bucket',required=True);p.add_argument('--key',required=True);p.add_argument('--credentials',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
 a.output.touch(mode=0o600,exist_ok=False);a.output.write_text(presign(a.endpoint,a.bucket,a.key,json.loads(a.credentials.read_text())))
 print('Private presigned S3 GET URL written to',a.output)
