---
id: builtin.design-doc
version: 1
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
    required: false
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 500
  maximum_body_chars: 30000
  heading_level: 2
generation:
  additional_instructions: |
    方案必须回应目标，包含失败路径、取舍、测试、发布与回滚。不得虚构接口字段或既有约束。
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
