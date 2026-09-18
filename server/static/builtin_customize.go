package static

// 内置全局自定义美化内容（源码集成版）
//
// 原先通过管理后台「全局设置 → 自定义头部 / 自定义正文」（customize_head /
// customize_body）注入页面，现在直接内置进源码、随二进制发布，不再依赖数据库设置项：
//
//   - 普通页面（/ 及分享页等）自动注入本文件内容；
//   - /@manage 管理页面不注入，避免影响后台界面（与原设置项注入行为一致）；
//   - 管理后台 customize_head / customize_body 中填写的内容会追加在内置内容之后，
//     建议清空后台这两个字段，避免脚本与样式重复加载；
//   - 看板娘（live2d）静态资源与模型库走国内镜像优先加载（jsdmirror → onmicrosoft
//     → fastly 兜底），加载器内联在 builtinCustomizeBody 中，可自行增删镜像；
//   - <style> 内选择器针对 HopeUI 运行时生成的类名（hope-c-... 等），
//     升级前端 dist 后类名可能变化，需要同步更新本文件。
//
// 原始来源：用户管理后台「全局自定义内容」。

const builtinCustomizeHead = `
<!-- ===== OpenList 内置美化：头部资源（server/static/builtin_customize.go）===== -->
<!--Alist V3建议添加的，已经默认添加了，如果你的没有建议加上-->
<script src="https://polyfill.alicdn.com/v3/polyfill.min.js?features=String.prototype.replaceAll"></script>

<!--引入字体，全局字体使用-->
<link rel="stylesheet" href="https://npm.elemecdn.com/lxgw-wenkai-webfont@1.1.0/lxgwwenkai-regular.css" />

<!-- Font6，自定义底部使用和看板娘使用的图标和字体文件-->
<link type="text/css" rel="stylesheet" href="https://npm.elemecdn.com/font6pro@6.3.0/css/fontawesome.min.css" media="all" />
<link href="https://npm.elemecdn.com/font6pro@6.3.0/css/all.min.css" rel="stylesheet" />

<!-- 看板娘核心文件（图标字体走国内镜像） -->
<link rel="stylesheet" href="https://cdn.jsdmirror.com/npm/font-awesome@4.7.0/css/font-awesome.min.css">

<!-- 看板娘样式适配 -->
<style>
  /* 原有样式保持不变 */
  /* 去除通知栏 右上角 X */
  .notify-render .hope-close-button {
    display: none;
  }
  /*去掉底部*/
  .footer {
    display: none !important;
  }

  /* 此选项两处CSS 在v3.31.0中已优化 滚动显示 和 右下角设置网格模式尺寸大小 */
  /* 文字超长自动换行 */
  /*.name-box .name {
    white-space: unset !important;
    overflow: unset !important;
    }*/
  /* 缩略图图片变大 代码中的160px 自己改 现在是注释状态若需要自行解除注释 */
  /*.obj-box > div {
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr))
    }
    .obj-box > div .item-thumbnail{
    height: 100px;
    }*/

  /*
    图片API用法点进去都会有食用说明的,API来自网络不保证实效性稳定性自己测试
    樱花：https://www.dmoe.cc
    夏沫：https://cdn.seovx.com
    搏天：https://api.btstu.cn/doc/sjbz.php
    姬长信：https://github.com/insoxin/API
    小歪：https://api.ixiaowai.cn/
    保罗：https://api.paugram.com
    墨天逸：https://api.mtyqx.cn
    岁月小筑：https://img.xjh.me
    东方Project：https://img.paulzzh.com
    */
  /*白天背景图*/
  .hope-ui-light {
    background-image: url("https://www.loliapi.com/acg/") !important;
    background-repeat: no-repeat;
    background-size: cover;
    background-attachment: fixed;
    background-position-x: center;
  }
  /*夜间背景图*/
  .hope-ui-dark {
    background-image: url("https://www.loliapi.com/acg/") !important;
    background-repeat: no-repeat;
    background-size: cover;
    background-attachment: fixed;
    background-position-x: center;
  }

  /*主列表白天模式透明*/
  .obj-box.hope-stack.hope-c-dhzjXW.hope-c-PJLV.hope-c-PJLV-igScBhH-css {
    background-color: rgba(255, 255, 255, 0.5) !important;
  }
  /*主列表夜间模式透明*/
  .obj-box.hope-stack.hope-c-dhzjXW.hope-c-PJLV.hope-c-PJLV-iigjoxS-css {
    background-color: rgb(0 0 0 / 50%) !important;
  }
  /*readme白天模式透明*/
  .hope-c-PJLV.hope-c-PJLV-ikSuVsl-css {
    background-color: rgba(255, 255, 255, 0.5) !important;
  }
  /*readme夜间模式透明*/
  .hope-c-PJLV.hope-c-PJLV-iiuDLME-css {
    background-color: rgb(0 0 0 / 50%) !important;
  }

  /*顶部右上角切换按钮透明*/
  .hope-ui-light .hope-c-ivMHWx-hZistB-cv.hope-icon-button {
    background-color: rgba(255, 255, 255, 0.5) !important;
  }
  .hope-ui-dark .hope-c-ivMHWx-hZistB-cv.hope-icon-button {
    background-color: rgb(0 0 0 / 50%) !important;
  }

  /*右下角侧边栏按钮透明 第一个是白天 第二个是夜间*/
  .hope-ui-light .hope-c-PJLV-ijgzmFG-css {
    background-color: rgba(255, 255, 255, 0.5) !important;
  }
  .hope-ui-dark .hope-c-PJLV-ijgzmFG-css {
    background-color: rgb(0 0 0 / 50%) !important;
  }
  /*白天模式代码块透明*/
  .hope-ui-light pre {
    background-color: rgba(255, 255, 255, 0.1) !important;
  }
  /*夜间模式代码块透明*/
  .hope-ui-dark pre {
    background-color: rgba(255, 255, 255, 0) !important;
  }

  /*左侧侧边栏目录*/
  /*白天模式*/
  .hope-ui-light .hope-c-PJLV-ieGWMbI-css {
    background: rgba(255, 255, 255, 0.5) !important;
  }
  /*夜间模式*/
  .hope-ui-dark .hope-c-PJLV-ieGWMbI-css {
    background-color: rgb(0 0 0 / 50%) !important;
  }
  /* 返回顶部 */
  .hope-c-PJLV-ihVEsOa-css {
    background: rgba(255, 255, 255, 0.5) !important;
  }
  .hope-ui-dark .hope-c-PJLV-ihVEsOa-css {
    background-color: rgb(0 0 0 / 50%) !important;
  }

  /*顶部*/
  #root > .header {
    background: rgba(255, 255, 255, 0);
  }
  /*导航条*/
  /*白天模式*/
  .hope-ui-light .body > .nav {
    background-color: rgba(255, 255, 255, 0.5);
    border-radius: var(--hope-radii-xl);
  }
  /*夜间模式*/
  .hope-ui-dark .body > .nav {
    background-color: rgb(0 0 0 / 50%);
    border-radius: var(--hope-radii-xl);
  }
  /*隐藏导航条遮罩*/
  .body > .nav::after {
    display: none;
  }

  /*底部CSS，.App .table这三个一起的*/
  dibu {
    border-top: 0px;
    position: absolute;
    bottom: 0;
    width: 100%;
    margin: 0px;
    padding: 0px;
  }
  .App {
    min-height: 85vh;
  }
  .table {
    margin: auto;
  }

  /*全局字体*/
  * {
    font-family: LXGW WenKai;
  }
  * {
    font-weight: bold;
  }
  body {
    font-family: LXGW WenKai;
  }

  /*以下为评论系统专用*/
  /*适配大小契合度*/
  .newValine {
    width: min(96%, 940px);
    flex-direction: column;
    row-gap: var(--hope-space-2);
    border-radius: var(--hope-radii-xl);
    padding: var(--hope-space-2);
    box-shadow: var(--hope-shadows-lg);
  }
  /*评论区 - 白天模式透明度*/
  .hope-ui-light .newValine {
    background-color: rgba(255, 255, 255, 0.5) !important;
  }
  /*评论区 - 夜间模式透明度*/
  .hope-ui-dark .newValine {
    background-color: rgb(0 0 0 / 50%) !important;
  }
  /*输入栏里面跳舞的小人背景图,jsdelivr加载慢的可以自己替换或者删掉*/
  .vedit {
    background-image: url("https://cdn.jsdelivr.net/gh/anwen-anyi/imgAnwen/images/OuNiJiang.gif");
    background-size: contain;
    background-repeat: no-repeat;
    background-position: right bottom;
    transition: all 0.25s ease-in-out 0s;
  }
  textarea#comment-textarea:focus {
    background-position-y: 120px;
    transition: all 0.25s ease-in-out 0s;
  }

  /*渐变背景CSS*/
  #canvas-basic {
    position: fixed;
    display: block;
    width: 100%;
    height: 100%;
    top: 0;
    right: 0;
    bottom: 0;
    left: 0;
    z-index: -999;
  }

  /* 以下为音乐播放器额外配置 */
  /* 如果你想要音乐播放器不是很靠底部可以自己设置一下数值 0是靠最底部 */
  .aplayer .aplayer-body,
  .aplayer.aplayer-withlist {
    bottom: 0rem !important;
  }
  /*音乐播放器进一步进行隐藏*/
  /* 需要就加不需要就不用加 */
  .aplayer.aplayer-fixed.aplayer-narrow .aplayer-body {
    left: -66px !important;
  }
  .aplayer.aplayer-fixed.aplayer-narrow .aplayer-body:hover {
    left: 0 !important;
  }
  
  /*白天模式 搜索主体+毛玻璃*/
  .hope-ui-light .hope-c-PJLV-iiBaxsN-css{
     background-color: rgba(255,255,255,0.2)!important;
     backdrop-filter: blur(10px)!important;
  }

  /*白天模式 搜索栏输入框+毛玻璃*/
  .hope-ui-light .hope-c-kvTTWD-hYRNAb-variant-filled{
     background-color: rgba(255,255,255,0.2)!important;
     backdrop-filter: blur(10px)!important;
  }

  /*白天模式 搜索按钮+毛玻璃*/
  .hope-ui-light .hope-c-PJLV-ikEIIxw-css{
     background-color: rgba(255,255,255,0.2)!important;
     backdrop-filter: blur(10px)!important;
     padding: var(--hope-space-1)!important;
  }

  /*夜间模式搜索主体+毛玻璃*/
  .hope-ui-dark .hope-c-PJLV-iiBaxsN-css{
      background-color: rgb(0 0 0 / 10%)!important;
      backdrop-filter: blur(10px)!important;
  }

  /*夜间模式搜索栏+毛玻璃*/
  .hope-ui-dark .hope-c-kvTTWD-hYRNAb-variant-filled{
      background-color: rgb(0 0 0 / 10%)!important;
      backdrop-filter: blur(10px)!important;
  }
/*夜间模式 搜索按钮+毛玻璃*/
  .hope-ui-dark .hope-c-PJLV-ikEIIxw-css{
      background-color: rgb(0 0 0 / 10%)!important;
      backdrop-filter: blur(10px)!important;
      padding: var(--hope-space-1)!important;
  }

  /* 看板娘样式适配 - 关键新增样式 */
  #live2d-widget {
    position: fixed !important;
    bottom: 0 !important;
    right: 0 !important;
    z-index: 999999 !important;
    pointer-events: auto !important;
  }
  
  /* 确保看板娘不被其他元素遮挡 */
  .live2d-widget-container {
    z-index: 999999 !important;
  }
  
  /* 适配移动端显示 */
  @media (max-width: 768px) {
    #live2d-widget {
      width: 120px !important;
      height: auto !important;
    }
  }
</style>
`

const builtinCustomizeBody = `
<!-- ===== OpenList 内置美化：看板娘容器（server/static/builtin_customize.go）===== -->
<div id="live2d-widget"></div>

<!-- 看板娘加载器（国内镜像优先：jsdmirror → onmicrosoft → fastly 兜底，任一镜像资源加载失败自动切换） -->
<script>
(function () {
  "use strict";
  if (screen.width < 768) return;
  var MIRRORS = [
    { assets: "https://cdn.jsdmirror.com/npm/live2d-widgets@0/",   models: "https://cdn.jsdmirror.com/gh/fghrsh/live2d_api/" },
    { assets: "https://jsd.onmicrosoft.cn/npm/live2d-widgets@0/",  models: "https://jsd.onmicrosoft.cn/gh/fghrsh/live2d_api/" },
    { assets: "https://fastly.jsdelivr.net/npm/live2d-widgets@0/", models: "https://fastly.jsdelivr.net/gh/fghrsh/live2d_api/" }
  ];
  function loadExternalResource(url, type) {
    return new Promise(function (resolve, reject) {
      var tag;
      if (type === "css") {
        tag = document.createElement("link");
        tag.rel = "stylesheet";
        tag.href = url;
      } else {
        tag = document.createElement("script");
        tag.src = url;
      }
      tag.onload = function () { resolve(url); };
      tag.onerror = function () { reject(url); };
      document.head.appendChild(tag);
    });
  }
  function tryMirror(i) {
    if (i >= MIRRORS.length) {
      console.warn("[live2d] all mirrors failed");
      return;
    }
    var m = MIRRORS[i];
    Promise.all([
      loadExternalResource(m.assets + "waifu.css", "css"),
      loadExternalResource(m.assets + "live2d.min.js", "js"),
      loadExternalResource(m.assets + "waifu-tips.js", "js")
    ]).then(function () {
      initWidget({
        waifuPath: m.assets + "waifu-tips.json",
        cdnPath: m.models,
        tools: ["hitokoto", "asteroids", "switch-model", "switch-texture", "photo", "info", "quit"]
      });
    }).catch(function () { tryMirror(i + 1); });
  }
  tryMirror(0);
})();
</script>
`
