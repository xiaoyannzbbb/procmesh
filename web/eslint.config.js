import js from '@eslint/js'
import tsPlugin from '@typescript-eslint/eslint-plugin'
import tsParser from '@typescript-eslint/parser'
import vuePlugin from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import i18nextPlugin from 'eslint-plugin-i18next'
import globals from 'globals'

export default [
  js.configs.recommended,
  {
    ignores: ['dist', 'node_modules', '.vite', 'src/gen']
  },
  {
    files: ['src/**/*.{js,ts,vue}'],
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        parser: tsParser,
        ecmaVersion: 2021,
        sourceType: 'module',
      },
      globals: {
        ...globals.browser,
        ...globals.node,
        HeadersInit: 'readonly',
        BlobPart: 'readonly',
      }
    },
    plugins: {
      '@typescript-eslint': tsPlugin,
      vue: vuePlugin,
      i18next: i18nextPlugin,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,
    }
  },
  {
    files: ['src/**/*.vue'],
    rules: {
      'i18next/no-literal-string': [
        'error',
        {
          framework: 'vue',
          mode: 'vue-template-only',
          'should-validate-template': false,
          words: {
            exclude: [
              /^[A-Z_-]+$/,
              /^(?:24h|7d|overview|config|logs|processIds|processGroup|action|start|stop|restart|kill|stderr|granted|denied|true|false|all|running|stopped|unhealthy|stale|name|owner|observed|health|revision|freshness|asc|m-)$/,
              /^(?:—|–|=|\/|×|→|·|:|,\s*)$/,
            ],
          },
          'jsx-attributes': {
            exclude: [
              'class',
              'style',
              'type',
              'role',
              'id',
              'data-testid',
              'data-action',
              /^data-[a-z0-9-]+$/,
              'aria-hidden',
              /^aria-[a-z0-9-]+$/,
              'tabindex',
              'autocomplete',
              'name',
              'href',
              'src',
              'to',
              'value',
              'kind',
              'unit',
              'spellcheck',
              'width',
              'height',
              'rows',
              'min',
              'max',
              'colspan',
              'fill',
              'stroke',
              'x1',
              'y1',
              'x2',
              'y2',
              'r',
              'offset',
              'stop-opacity',
              'stroke-width',
              'stop-color',
              'stroke-linejoin',
              'stroke-linecap',
              'vector-effect',
              'preserveAspectRatio',
              'size',
            ],
          },
        },
      ],
    }
  }
]
