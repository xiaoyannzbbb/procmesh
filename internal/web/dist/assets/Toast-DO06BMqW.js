import{d as _,x as b,v as I,p as u,b as m,M as T,y as g,T as w,r as f,o as n,c as x,m as i,D as B,t as z,u as p,k as M,q as E,s as d}from"./vue-core-iJmBE1T9.js";import{u as L,_ as N}from"./_plugin-vue_export-helper-D3Hs5g6M.js";import{X as V}from"./x-ZRRaK2SV.js";import{c as l}from"./createLucideIcon-CKMzhuwj.js";import{C as D}from"./circle-alert-CWDDg_eX.js";/**
 * @license lucide-vue-next v0.469.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const X=l("CircleCheckBigIcon",[["path",{d:"M21.801 10A10 10 0 1 1 17 3.335",key:"yps3ct"}],["path",{d:"m9 11 3 3L22 4",key:"1pflzl"}]]);/**
 * @license lucide-vue-next v0.469.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const q=l("CircleXIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m15 9-6 6",key:"1uzhvr"}],["path",{d:"m9 9 6 6",key:"z0biqf"}]]);/**
 * @license lucide-vue-next v0.469.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const A=l("InfoIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"M12 16v-4",key:"1dtifu"}],["path",{d:"M12 8h.01",key:"e9boi3"}]]),O={class:"toast-message"},G=["aria-label"],P="polite",R=_({__name:"Toast",props:{show:{type:Boolean},message:{},type:{},duration:{}},emits:["close"],setup(o,{emit:y}){const t=o,v=y,{t:h}=L(),a=f(!1),e=f(null),k=d(()=>{switch(t.type){case"success":return X;case"error":return q;case"warning":return D;default:return A}}),C=d(()=>{switch(t.type){case"success":return"#10b981";case"error":return"#ef4444";case"warning":return"#f59e0b";default:return"#3b82f6"}});function c(){a.value=!1,e.value&&(clearTimeout(e.value),e.value=null),setTimeout(()=>v("close"),300)}return b(()=>t.show,s=>{if(s){a.value=!0,e.value&&clearTimeout(e.value);const r=t.duration??5e3;r>0&&(e.value=setTimeout(c,r))}else a.value=!1},{immediate:!0}),I(()=>{if(t.show){a.value=!0;const s=t.duration??5e3;s>0&&(e.value=setTimeout(c,s))}}),(s,r)=>(n(),u(w,{to:"body"},[m(T,{name:"toast"},{default:g(()=>[a.value?(n(),x("div",{key:0,class:"toast-container",role:"alert","aria-live":P},[i("div",{class:M(["toast",`toast-${o.type||"info"}`])},[(n(),u(B(k.value),{size:20,color:C.value,class:"toast-icon"},null,8,["color"])),i("p",O,z(o.message),1),i("button",{type:"button",class:"toast-close","aria-label":p(h)("actions.close"),onClick:c},[m(p(V),{size:18})],8,G)],2)])):E("",!0)]),_:1})]))}}),J=N(R,[["__scopeId","data-v-4860c165"]]);export{A as I,J as T};
