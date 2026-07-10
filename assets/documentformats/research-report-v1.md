---
id: builtin.research-report
version: 1
name: 调研报告
family: research
description: 用于回答研究问题、比较对象、汇总证据并形成结论
enabled: true
auto_match:
  enabled: true
  priority: 20
  use_when:
    - 用户明确要求调研报告、研究报告或对比分析
  positive_examples:
    - 把以上内容整理成调研报告
  negative_examples:
    - 简单记录一个调研想法
inputs:
  - key: research_question
    label: 研究问题
    required: true
  - key: scope
    label: 研究范围
    required: false
  - key: audience
    label: 目标读者
    required: false
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 300
  maximum_body_chars: 30000
  heading_level: 2
generation:
  additional_instructions: |
    区分事实、推断和建议。不得伪造来源；没有来源支持的判断必须标记为假设或局限。
---
# {{title}}

> {{summary}}

## 研究摘要

{{section:abstract|required}}

## 研究问题与范围

{{section:scope|required}}

## 研究方法与信息边界

{{section:method|required}}

## 主要发现

{{section:findings|required}}

## 对比与分析

{{section:analysis|required}}

## 结论与建议

{{section:conclusion|required}}

## 局限性

{{section:limitations|required}}

## 参考来源

{{references}}
