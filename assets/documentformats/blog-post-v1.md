---
id: builtin.blog-post
version: 1
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
  - key: tone
    label: 语气
    required: false
validation:
  allow_unknown_sections: false
  require_non_empty_sections: true
  minimum_body_chars: 300
  maximum_body_chars: 30000
  heading_level: 2
generation:
  additional_instructions: |
    使用面向读者的连贯叙事，不要把报告目录直接改写成博客正文。
---
# {{title}}

{{section:lead|required}}

{{section:body|required}}

{{section:example|required}}

{{section:conclusion|required}}

{{references}}
