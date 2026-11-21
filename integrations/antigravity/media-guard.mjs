#!/usr/bin/env node
import{readFileSync}from"node:fs";import{parseArgs}from"node:util";import{decide}from"./lib/media-guard.js";const{values}=parseArgs({options:{state:{type:"string"}},strict:!1});let payload;try{payload=JSON.parse(readFileSync(0,"utf8"))}catch{}process.stdout.write(JSON.stringify(decide(payload,values.state))+`
`);
