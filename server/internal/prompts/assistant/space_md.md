{{if eq .Lang "en"}}# {{.Title}}

## What this space is for

## How to answer

## File conventions

{{else if eq .Lang "ja"}}# {{.Title}}

## このスペースの目的

## 答え方

## ファイルの決まり

{{else}}# {{.Title}}

## 这个空间做什么

## 怎么回答

## 文件约定

{{end}}<!-- persona:managed:state:begin -->
<!-- persona:managed:state:end -->

