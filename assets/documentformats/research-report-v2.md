---
schema_version: 2
id: builtin.research-report
version: 2
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
  - key: purpose
    label: 用途
    required: true
  - key: scope
    label: 范围
    required: true
  - key: comparison
    label: 比较对象与维度（无比较时填写不适用）
    required: true
  - key: cutoff
    label: 信息截止时间
    required: true
  - key: audience
    label: 目标读者
    required: true
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 300
  maximum_body_chars: 50000
  heading_level: 2
  rules:
    - research_evidence
  section_headings:
    abstract: 摘要
    scope: 问题与边界
    method: 研究方法
    findings: 发现与证据
    analysis: 分析与比较
    conclusion: 结论与建议
    limitations: 局限与后续验证
generation:
  additional_instructions: |
    使用 Q1、F1、C1 标识问题、发现和结论，引用 S1 等来源；保留冲突证据、比较条件及不确定性，无法获取的来源标注未核验。
---
# {{title}}

> {{summary}}

## 摘要

{{section:abstract|required}}

## 问题与边界

{{section:scope|required}}

## 研究方法

{{section:method|required}}

## 发现与证据

{{section:findings|required}}

## 分析与比较

{{section:analysis|required}}

## 结论与建议

{{section:conclusion|required}}

## 局限与后续验证

{{section:limitations|required}}

{{references}}
