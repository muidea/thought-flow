---
schema_version: 2
id: builtin.design-doc
version: 2
name: 设计文档
family: design
description: 用于产品、系统、API、数据模型、流程或工程方案设计
enabled: true
auto_match:
  enabled: true
  priority: 20
  use_when:
    - 用户明确要求设计文档、技术方案、架构设计或 RFC
  positive_examples:
    - 把讨论整理成系统设计文档
  negative_examples:
    - 写一篇介绍系统设计的博客
inputs:
  - key: problem
    label: 问题
    required: true
  - key: goals
    label: 目标
    required: true
  - key: non_goals
    label: 非目标
    required: true
  - key: audience
    label: 目标读者
    required: true
  - key: context
    label: 现有系统或适用场景
    required: true
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 300
  maximum_body_chars: 50000
  heading_level: 2
  rules:
    - design_traceability
  section_headings:
    background: 背景与问题
    goals: 目标
    non_goals: 非目标
    constraints: 约束
    proposal: 方案设计
    alternatives: 备选方案与取舍
    risks: 风险与应对
    testing: 测试方案
    rollout: 发布与回滚
    open_questions: 待决问题
generation:
  additional_instructions: |
    使用 G1、D1、T1 标识目标、决策和验收测试，正文明确相互引用；记录失败及恢复。未知项如实列明，不虚构。
---
# {{title}}

> {{summary}}

## 背景与问题

{{section:background|required}}

## 目标

{{section:goals|required}}

## 非目标

{{section:non_goals|required}}

## 约束

{{section:constraints|required}}

## 方案设计

{{section:proposal|required}}

## 备选方案与取舍

{{section:alternatives|required}}

## 风险与应对

{{section:risks|required}}

## 测试方案

{{section:testing|required}}

## 发布与回滚

{{section:rollout|required}}

## 待决问题

{{section:open_questions|required}}

{{references}}
