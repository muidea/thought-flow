---
id: builtin.note
version: 1
name: 通用笔记
family: note
description: 用于一般知识记录、会议结论、短总结和个人思考
enabled: true
auto_match:
  enabled: true
  priority: 0
  use_when:
    - 用户希望保存普通笔记、记录或总结
validation:
  allow_unknown_sections: true
  require_non_empty_sections: true
  minimum_body_chars: 1
  maximum_body_chars: 50000
  heading_level: 2
---
# {{title}}

{{section:body|required}}

{{references}}
