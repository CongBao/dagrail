import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {dirname, join, normalize} from 'node:path';

const here=dirname(fileURLToPath(import.meta.url));
const staticRoot=normalize(join(here,'..','..','static'));
const snapshotRef='snapshot:visual-golden-v026';
const graphRevision='2b76397a9baf01328eeff74adf943d8d9427998ca45aec95e4c9bc18b9d8107d';

const group=(id,title,displayStatus,nodeCount)=>({
  id,title,kind:'work-unit',laneId:'delivery',collapsedByDefault:true,visible:true,
  expanded:false,breadcrumbs:[id],childGroupIds:[],lifecycleStatus:displayStatus,
  healthStatus:displayStatus==='blocked'?'attention':'clean',displayStatus,nodeCount,
  directNodeCount:nodeCount,readyNodeCount:displayStatus==='ready'?1:0,
  activeNodeCount:displayStatus==='active'?1:0,attemptCount:displayStatus==='active'?1:0,
  activeAttemptCount:displayStatus==='active'?1:0,currentCycle:0,
  openIncidentCount:displayStatus==='blocked'?1:0,uncertainEffectCount:0,
  unclosedResourceCount:0,membershipDigest:`sha256:${id.padEnd(64,'0').slice(0,64)}`,
  internalMatchCount:nodeCount,
});
const groups=[
  group('foundation','Foundation','completed',4),
  group('build','Build capability','active',5),
  group('review','Independent review','ready',3),
  group('release','Release candidate','blocked',4),
  group('observe','Operational validation','planned',3),
  group('complete','Completion','planned',2),
];
const globalNodes=[{
  id:'release-gate',kind:'gate',title:'Release gate',laneId:'milestones',status:'planned',
  readiness:'blocked',groupId:'',role:'',objective:'',outcomes:[{id:'passed',class:'success'}],
}];
const aggregateEdges=[
  ['foundation','build'],['build','review'],['review','release'],['release','release-gate'],
  ['release-gate','observe'],['observe','complete'],
].map(([from,to],index)=>({
  fromRef:`${from==='release-gate'?'node':'group'}:${from}`,
  toRef:`${to==='release-gate'?'node':'group'}:${to}`,
  predicateKind:'outcome',outcomeClass:'success',count:index===1?3:1,
  edgeDigest:`edge-${String(index+1).padStart(2,'0')}`,
}));
const projectMap={
  apiVersion:'dagrail.io/ui/v1beta3',kind:'ProjectMap',readOnly:true,
  project:{id:'2cfce415-9442-4c5a-b642-a33fd08706dd',name:'DAGrail visual fixture'},
  headSequence:126,headHash:'visual-head',graphRevision,snapshotRef,
  generatedAt:'2026-08-19T00:00:00Z',groups,nodes:globalNodes,
  lanes:[
    {id:'delivery',title:'Delivery flow',groupRefs:groups.map(value=>`group:${value.id}`),nodeRefs:[]},
    {id:'milestones',title:'Milestones & gates',groupRefs:[],nodeRefs:['node:release-gate']},
  ],
  aggregateEdges,aggregateEdgePage:{count:aggregateEdges.length,complete:true},aggregateEdgeIndexRef:'',
};

function internalNodes(id){
  const labels=id==='build'?['plan','implement','test','review','complete']:['prepare','verify','publish','complete'];
  return labels.map((label,index)=>({
    id:`${id}-${label}`,kind:label==='review'?'review':label==='complete'?'milestone':'task',
    title:`${label[0].toUpperCase()}${label.slice(1)} ${id}`,groupId:id,role:index<2?'developer':'reviewer',
    status:index===0?'terminal':'planned',readiness:index===1?'ready':'blocked',
    objective:'',outcomes:[{id:'done',class:'success'}],
  }));
}
function groupMembers(id){
  const nodes=internalNodes(id);
  const internalEdges=nodes.slice(0,-1).map((node,index)=>({
    fromRef:`node:${node.id}`,toRef:`node:${nodes[index+1].id}`,predicateKind:'outcome',
    outcomeClass:'success',count:1,edgeDigest:`${id}-internal-${index}`,
  }));
  return {
    apiVersion:'dagrail.io/ui/v1beta3',kind:'GroupMembers',snapshotRef,
    topology:{
      groups:groups.map(value=>({...value,expanded:value.id===id})),nodes:[...globalNodes,...nodes],
      lanes:projectMap.lanes,aggregateEdges:[...aggregateEdges,...internalEdges],
      aggregateEdgeIndexRef:'',headSequence:126,graphRevision,page:{count:nodes.length,complete:true},
    },
  };
}

const send=(response,status,value)=>{
  const body=JSON.stringify(value);
  response.writeHead(status,{'content-type':'application/json; charset=utf-8','cache-control':'no-store'});
  response.end(body);
};

createServer(async(request,response)=>{
  const url=new URL(request.url,'http://127.0.0.1:41736');
  if(url.pathname==='/api/v1/project-map')return send(response,200,projectMap);
  if(url.pathname==='/api/v1/group-members')return send(response,200,groupMembers(url.searchParams.get('id')||'build'));
  if(url.pathname==='/api/v1/head')return send(response,200,{headSequence:126,headHash:'visual-head',graphRevision,snapshotAvailable:true,snapshotSequence:126,changed:false});
  if(url.pathname==='/api/v1/locate')return send(response,200,{snapshotRef,results:[{id:'build-implement',kind:'node',title:'Implement build',ancestorPath:['build'],internalMatchCount:1}]});
  if(url.pathname==='/api/v1/node')return send(response,200,{headSequence:126,graphRevision,contract:internalNodes('build').find(node=>node.id===url.searchParams.get('id'))||internalNodes('release')[0],runtime:{status:'planned',outcome:''},readiness:{state:'ready',reasons:[]},counts:{attempts:1,incidents:0,effects:0,resources:0,incoming:1,outgoing:1}});
  if(url.pathname.startsWith('/api/'))return send(response,404,{error:'fixture route not found'});
  const asset=url.pathname==='/'?'index.html':url.pathname.replace(/^\/assets\//,'');
  if(asset.includes('..'))return send(response,404,{error:'not found'});
  try{
    const body=await readFile(join(staticRoot,asset));
    const type=asset.endsWith('.css')?'text/css':asset.endsWith('.js')?'text/javascript':'text/html';
    response.writeHead(200,{'content-type':`${type}; charset=utf-8`,'cache-control':'no-store'});response.end(body);
  }catch{send(response,404,{error:'not found'});}
}).listen(41736,'127.0.0.1');
