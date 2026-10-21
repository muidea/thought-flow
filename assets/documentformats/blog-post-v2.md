---
schema_version: 2
id: builtin.blog-post
version: 2
name: 博文
family: article
description: 用于面向明确读者传达核心观点的博客文章
enabled: true
auto_match:
  enabled: true
  priority: 20
  use_when:
    - 用户明确要求写成博文、博客文章或技术文章
  positive_examples:
    - 把以上内容写成面向开发者的博文
  negative_examples:
    - 讨论博客应该怎么写
inputs:
  - key: audience
    label: 目标读者
    required: true
  - key: core_message
    label: 核心观点
    required: true
  - key: purpose
    label: 文章用途
    required: true
  - key: subtype
    label: 子类型：opinion / tutorial / case
    required: true
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 300
  maximum_body_chars: 50000
  heading_level: 2
  rules:
    - article_subtype
  section_headings:
    lead: 引言
    body: 论证
    example: 示例
    conclusion: 结语
generation:
  additional_instructions: |
    子类型按 subtype 参数选择。教程必须说明环境、输入、步骤、预期输出、验证、失败；未执行的示例标注“未运行验证”。案例区分真实、匿名化与假设。观点讨论反例和边界，不强塞部署章节。
---
# {{title}}

> {{summary}}

## 引言

{{section:lead|required}}

## 论证

{{section:body|required}}

## 示例

{{section:example|required}}

## 结语

{{section:conclusion|required}}

{{references}}
