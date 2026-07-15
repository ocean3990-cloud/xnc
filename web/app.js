const FIELDS = ['fullName','birthDate','birthPrecision','gender','nationality','passport','room','arrival','departure','checkout'];
const FIELD_LABELS = {
  fullName:'Họ tên', birthDate:'Ngày sinh', birthPrecision:'Ngày sinh đúng đến', gender:'Giới tính',
  nationality:'Quốc tịch', passport:'Số hộ chiếu', room:'Số phòng', arrival:'Ngày đến',
  departure:'Ngày đi dự kiến', checkout:'Ngày trả phòng'
};

let guests = [];
let activeId = null;
let nextId = 1;
let aliasMap = new Map();
let countryByCode = new Map();
let countryNameToCode = new Map();
let genericWorker = null;
let mrzWorker = null;
let currentSearch = '';
let settings = loadSettings();

const $ = (id) => document.getElementById(id);
const guestBody = $('guestBody');
const detailInputs = [...document.querySelectorAll('#detailForm [data-field]')];

function loadSettings(){
  try { return Object.assign({autoUppercase:true, copyDepartureToCheckout:false, ocrVietnamese:true, skipBlankArrivalOnly:true}, JSON.parse(localStorage.getItem('xnc_settings')||'{}')); }
  catch { return {autoUppercase:true, copyDepartureToCheckout:false, ocrVietnamese:true, skipBlankArrivalOnly:true}; }
}
function saveSettings(){ localStorage.setItem('xnc_settings', JSON.stringify(settings)); }

function normalizeKey(value){
  return String(value ?? '').normalize('NFD').replace(/[\u0300-\u036f]/g,'').replace(/đ/g,'d').replace(/Đ/g,'D')
    .toLowerCase().replace(/[^a-z0-9]+/g,' ').trim();
}
function cleanText(value){ return String(value ?? '').replace(/\s+/g,' ').trim(); }
function todayDate(){ const d=new Date(); return `${String(d.getDate()).padStart(2,'0')}/${String(d.getMonth()+1).padStart(2,'0')}/${d.getFullYear()}`; }
function upper(value){ return cleanText(value).toUpperCase(); }
function escapeHtml(value){ return String(value ?? '').replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
function xmlEscape(value){ return String(value ?? '').replace(/[<>&'\"]/g, c => ({'<':'&lt;','>':'&gt;','&':'&amp;',"'":'&apos;','"':'&quot;'}[c])); }
function isVietnameseNationality(value){
  const n = normalizeKey(value).replace(/\s/g,'');
  return ['vnm','vn','vietnam','vietname','vietnames','viet'].includes(n) || n.startsWith('vnm');
}

function excelSerialToDate(n){
  const serial=Number(n); if(!Number.isFinite(serial)) return '';
  const d=new Date(Date.UTC(1899,11,30)+Math.round(serial)*86400000);
  return `${String(d.getUTCDate()).padStart(2,'0')}/${String(d.getUTCMonth()+1).padStart(2,'0')}/${d.getUTCFullYear()}`;
}
function validCalendarDate(day,month,year){
  const d = new Date(year, month-1, day);
  return d.getFullYear()===year && d.getMonth()===month-1 && d.getDate()===day;
}
function parseDateValue(value, allowPartial=false){
  if(value instanceof Date && !isNaN(value)){
    return {value:`${String(value.getDate()).padStart(2,'0')}/${String(value.getMonth()+1).padStart(2,'0')}/${value.getFullYear()}`, precision:'D'};
  }
  if(typeof value==='number' && value>1000 && value<100000) return {value:excelSerialToDate(value),precision:'D'};
  let s = cleanText(value).replace(/[.\-]/g,'/').replace(/\s+00:00:00$/,'');
  if(!s) return {value:'',precision:''};
  let m;
  if((m=s.match(/^(\d{4})\/(\d{1,2})\/(\d{1,2})$/))){
    const [,y,mo,d]=m; s=`${d.padStart(2,'0')}/${mo.padStart(2,'0')}/${y}`;
  }
  if((m=s.match(/^(\d{1,2})\/(\d{1,2})\/(\d{2}|\d{4})$/))){
    let [,d,mo,y]=m; if(y.length===2) y=(Number(y)>30?'19':'20')+y;
    return {value:`${d.padStart(2,'0')}/${mo.padStart(2,'0')}/${y}`,precision:'D'};
  }
  if(allowPartial && (m=s.match(/^(\d{1,2})\/(\d{4})$/))) return {value:`${m[1].padStart(2,'0')}/${m[2]}`,precision:'M'};
  if(allowPartial && /^\d{4}$/.test(s)) return {value:s,precision:'Y'};
  const parsed = new Date(s);
  if(!isNaN(parsed) && /[A-Za-z]/.test(s)) return {value:`${String(parsed.getDate()).padStart(2,'0')}/${String(parsed.getMonth()+1).padStart(2,'0')}/${parsed.getFullYear()}`,precision:'D'};
  return {value:s,precision:allowPartial?'D':''};
}
function isValidDate(value, precision='D'){
  if(!value) return false;
  if(precision==='Y') return /^(18|19|20)\d{2}$/.test(value);
  if(precision==='M'){
    const m=value.match(/^(\d{2})\/(\d{4})$/); return !!m && Number(m[1])>=1 && Number(m[1])<=12;
  }
  const m=value.match(/^(\d{2})\/(\d{2})\/(\d{4})$/);
  return !!m && validCalendarDate(Number(m[1]),Number(m[2]),Number(m[3]));
}
function dateToNumber(value){
  const m=String(value).match(/^(\d{2})\/(\d{2})\/(\d{4})$/); if(!m) return NaN;
  return new Date(Number(m[3]),Number(m[2])-1,Number(m[1])).getTime();
}
function normalizeGender(value){
  const n=normalizeKey(value);
  if(['m','male','nam','man','masculin','masculino'].includes(n)) return 'M';
  if(['f','female','nu','woman','feminin','femenino'].includes(n)) return 'F';
  if(/^m\b/.test(n)) return 'M'; if(/^f\b/.test(n)) return 'F';
  return '';
}
function normalizeNationality(value){
  const raw=cleanText(value); if(!raw) return '';
  if(isVietnameseNationality(raw)) return 'VNM';
  const codeMatch=raw.toUpperCase().match(/^([A-Z]{3})(?:\s*-|\s|$)/);
  if(codeMatch) return codeMatch[1];
  const exact=raw.toUpperCase(); if(/^[A-Z]{3}$/.test(exact)) return exact;
  const byName=countryNameToCode.get(normalizeKey(raw));
  return byName || exact.slice(0,20);
}
// Common OCR letter/digit confusions (bidirectional) for nationality codes.
const OCR_AMBIG={A:['4'],'4':['A'],O:['0'],'0':['O'],I:['1'],'1':['I'],S:['5'],'5':['S'],B:['8'],'8':['B'],G:['6'],'6':['G'],Z:['2'],'2':['Z']};
// correctNationalityCode: for a 3-char code that is NOT in the export template,
// try the OCR swaps above. Returns {corrected:true, code} only when EXACTLY one
// valid template code results (e.g. US4→USA, U5A→USA). If several valid codes are
// reachable, or none, returns {corrected:false, candidates:[...]} so staff can pick.
function correctNationalityCode(cand){
  cand=String(cand||'').toUpperCase().replace(/[^A-Z0-9]/g,'');
  if(cand.length!==3) return {corrected:false, candidates:nearNationalityCodes(cand)};
  const found=new Set();
  const opts=[...cand].map(ch=>[ch,...(OCR_AMBIG[ch]||[])]);
  for(const a of opts[0])for(const b of opts[1])for(const c of opts[2]){
    const code=a+b+c;
    if(/^[A-Z]{3}$/.test(code) && countryByCode.has(code)) found.add(code);
  }
  const arr=[...found];
  if(arr.length===1) return {corrected:true, code:arr[0]};
  return {corrected:false, candidates:arr.length?arr:nearNationalityCodes(cand)};
}
// nearNationalityCodes: valid template codes that differ from cand in at most one
// position (used for the red "pick a code" list when correction is inconclusive).
function nearNationalityCodes(cand){
  cand=String(cand||'').toUpperCase().replace(/[^A-Z0-9]/g,'').slice(0,3);
  if(cand.length!==3) return [];
  const out=[];
  for(const code of countryByCode.keys()){
    let d=0; for(let i=0;i<3;i++) if(code[i]!==cand[i]) d++;
    if(d<=1){ out.push(code); if(out.length>=10) break; }
  }
  return out;
}
function normalizePassport(value){ return upper(value).replace(/[^A-Z0-9]/g,''); }
function normalizeRoom(value){ return upper(value).replace(/\s+/g,''); }
function normalizeName(value){ return settings.autoUppercase ? upper(value) : cleanText(value); }

function makeGuest(input={}, source={}){
  const birth=parseDateValue(input.birthDate,true);
  const arrival=parseDateValue(input.arrival,false).value || todayDate();
  const departure=parseDateValue(input.departure,false).value;
  let checkout=parseDateValue(input.checkout,false).value;
  if(!checkout && settings.copyDepartureToCheckout && departure) checkout=departure;
  return {
    id:nextId++, selected:true,
    fullName:normalizeName(input.fullName), birthDate:birth.value,
    birthPrecision:(input.birthPrecision||birth.precision||'').toString().charAt(0).toUpperCase(),
    gender:normalizeGender(input.gender), nationality:normalizeNationality(input.nationality),
    passport:normalizePassport(input.passport), room:normalizeRoom(input.room),
    arrival, departure, checkout,
    forceReview:!!input.forceReview, sourceName:source.name||'', sourceType:source.type||'', preview:source.preview||'',
    confidence:source.confidence??null, status:'review', reasons:[], natNote:'', natInvalid:false, natCandidates:[]
  };
}

function addGuests(records, source){
  const added=[];
  for(const rec of records){
    if(!rec || !Object.values(rec).some(v=>cleanText(v))) continue;
    const g=makeGuest(rec,source);
    if(isVietnameseNationality(g.nationality)) g.selected=false;
    guests.push(g); added.push(g);
  }
  validateAll();
  if(added.length){ activeId=added[0].id; renderAll(); toast(`Đã thêm ${added.length} khách từ ${source.name||'nguồn dữ liệu'}`,'ok'); }
  else { renderAll(); toast('Không tìm thấy dòng khách hợp lệ trong file','bad'); }
  return added.length;
}

function validateAll(){
  const passports=new Map();
  for(const g of guests){ if(g.passport) passports.set(g.passport,(passports.get(g.passport)||0)+1); }
  for(const g of guests){
    const reasons=[];
    if(isVietnameseNationality(g.nationality)){
      g.status='excluded'; g.reasons=['Quốc tịch Việt Nam']; g.selected=false; continue;
    }
    if(!g.fullName) reasons.push('Thiếu họ tên');
    if(!g.birthDate) reasons.push('Thiếu ngày sinh'); else if(!isValidDate(g.birthDate,g.birthPrecision)) reasons.push('Ngày sinh không hợp lệ');
    if(!['D','M','Y'].includes(g.birthPrecision)) reasons.push('Sai độ chính xác ngày sinh');
    if(!['M','F'].includes(g.gender)) reasons.push('Thiếu giới tính');
    if(!g.nationality){ reasons.push('Thiếu quốc tịch'); g.natInvalid=false; g.natCandidates=[]; g.natNote=''; }
    else if(!countryByCode.size){ if(!/^[A-Z]{3}$/.test(g.nationality)) reasons.push('Mã quốc tịch phải gồm 3 chữ cái'); }
    else if(countryByCode.has(g.nationality)){ g.natInvalid=false; g.natCandidates=[]; }
    else {
      const fix=correctNationalityCode(g.nationality);
      if(fix.corrected){ g.natNote=`${g.nationality} đã được hiệu chỉnh thành ${fix.code}.`; g.nationality=fix.code; g.natInvalid=false; g.natCandidates=[]; }
      else { reasons.push('Mã quốc tịch không hợp lệ'); g.natInvalid=true; g.natCandidates=fix.candidates||[]; g.natNote=''; }
    }
    if(!g.passport) reasons.push('Thiếu số hộ chiếu');
    if(!g.room) reasons.push('Thiếu số phòng');
    if(!g.arrival) reasons.push('Thiếu ngày đến'); else if(!isValidDate(g.arrival,'D')) reasons.push('Ngày đến không hợp lệ');
    if(!g.departure) reasons.push('Thiếu ngày đi dự kiến'); else if(!isValidDate(g.departure,'D')) reasons.push('Ngày đi dự kiến không hợp lệ');
    if(!g.checkout) reasons.push('Thiếu ngày trả phòng'); else if(!isValidDate(g.checkout,'D')) reasons.push('Ngày trả phòng không hợp lệ');
    const a=dateToNumber(g.arrival),d=dateToNumber(g.departure),c=dateToNumber(g.checkout);
    if(Number.isFinite(a)&&Number.isFinite(d)&&d<a) reasons.push('Ngày đi trước ngày đến');
    if(Number.isFinite(a)&&Number.isFinite(c)&&c<a) reasons.push('Ngày trả phòng trước ngày đến');
    if(g.passport && passports.get(g.passport)>1) reasons.push('Trùng số hộ chiếu');
    if(g.forceReview) reasons.push('Được đánh dấu kiểm tra thủ công');
    g.reasons=[...new Set(reasons)]; g.status=reasons.length?'review':'ok';
  }
}

function renderAll(){ validateAll(); renderTable(); renderDetails(); renderCounters(); }
function filteredGuests(){
  if(!currentSearch) return guests;
  const q=normalizeKey(currentSearch);
  return guests.filter(g=>FIELDS.some(f=>normalizeKey(g[f]).includes(q)) || normalizeKey(g.sourceName).includes(q));
}
function renderTable(){
  const rows=filteredGuests();
  $('emptyState').classList.toggle('hidden',guests.length>0);
  guestBody.innerHTML=rows.map((g,index)=>{
    const rowClass=[g.id===activeId?'active':'',g.status==='excluded'?'excluded':''].join(' ');
    const statusText=g.status==='ok'?'Đủ dữ liệu':g.status==='excluded'?'Bị loại (VN)':'Cần kiểm tra';
    const statusClass=g.status==='ok'?'ok':g.status==='excluded'?'excluded':'review';
    return `<tr data-id="${g.id}" class="${rowClass}" title="${escapeHtml(g.reasons.join('; '))}">
      <td class="check-col"><input type="checkbox" data-select="${g.id}" ${g.selected?'checked':''} ${g.status==='excluded'?'disabled':''}></td>
      <td class="stt-col">${guests.indexOf(g)+1}</td>
      <td contenteditable="true" data-field="fullName">${escapeHtml(g.fullName)}</td>
      <td contenteditable="true" data-field="birthDate">${escapeHtml(g.birthDate)}</td>
      <td><select class="grid-select" data-field="birthPrecision"><option value="" ${!g.birthPrecision?'selected':''}></option><option ${g.birthPrecision==='D'?'selected':''}>D</option><option ${g.birthPrecision==='M'?'selected':''}>M</option><option ${g.birthPrecision==='Y'?'selected':''}>Y</option></select></td>
      <td><select class="grid-select" data-field="gender"><option value=""></option><option value="M" ${g.gender==='M'?'selected':''}>Nam</option><option value="F" ${g.gender==='F'?'selected':''}>Nữ</option></select></td>
      <td contenteditable="true" data-field="nationality" class="${g.natInvalid?'nat-bad':''}">${escapeHtml(g.nationality)}</td>
      <td contenteditable="true" data-field="passport">${escapeHtml(g.passport)}</td>
      <td contenteditable="true" data-field="room">${escapeHtml(g.room)}</td>
      <td contenteditable="true" data-field="arrival">${escapeHtml(g.arrival)}</td>
      <td contenteditable="true" data-field="departure">${escapeHtml(g.departure)}</td>
      <td contenteditable="true" data-field="checkout">${escapeHtml(g.checkout)}</td>
      <td><span class="status ${statusClass}">${statusText}</span></td>
    </tr>`;
  }).join('');
  guestBody.querySelectorAll('tr').forEach(tr=>tr.addEventListener('click',e=>{
    if(e.target.matches('input[type=checkbox],select')||e.target.isContentEditable) return;
    activeId=Number(tr.dataset.id); renderTable(); renderDetails();
  }));
  guestBody.querySelectorAll('[data-select]').forEach(cb=>cb.addEventListener('change',e=>{
    const g=findGuest(Number(cb.dataset.select)); if(g) g.selected=cb.checked; renderCounters();
  }));
  guestBody.querySelectorAll('td[contenteditable=true]').forEach(td=>{
    td.addEventListener('focus',()=>{ activeId=Number(td.closest('tr').dataset.id); renderDetails(); });
    td.addEventListener('blur',()=>{
      const g=findGuest(Number(td.closest('tr').dataset.id)); if(!g)return;
      updateGuestField(g,td.dataset.field,td.textContent); renderAll();
    });
    td.addEventListener('keydown',e=>{ if(e.key==='Enter'){e.preventDefault();td.blur();} });
    td.addEventListener('paste',e=>{
      const text=e.clipboardData?.getData('text/plain')||'';
      if(!/[\t\r\n]/.test(text)) return;
      e.preventDefault();
      pasteGridData(Number(td.closest('tr').dataset.id),td.dataset.field,text);
    });
  });
  guestBody.querySelectorAll('.grid-select').forEach(sel=>sel.addEventListener('change',()=>{
    const g=findGuest(Number(sel.closest('tr').dataset.id)); if(!g)return;
    updateGuestField(g,sel.dataset.field,sel.value); renderAll();
  }));
}
function renderDetails(){
  const g=findGuest(activeId);
  for(const el of detailInputs){
    const field=el.dataset.field;
    if(el.type==='checkbox') el.checked=g?!!g[field]:false;
    else el.value=g?(g[field]??''):'';
    el.disabled=!g;
  }
  const empty=$('viewerEmpty');
  if(g&&g.preview){ if(empty)empty.hidden=true; if(inlineViewer)inlineViewer.load(g.preview); }
  else { if(empty)empty.hidden=false; if(inlineViewer)inlineViewer.load(''); }
  if(cropMode) cancelCrop();
  const natEl=document.querySelector('#detailForm input[data-field="nationality"]');
  if(natEl) natEl.classList.toggle('invalid', !!(g&&g.natInvalid));
  const hint=$('natHint');
  if(hint){
    if(g&&g.natNote){ hint.hidden=false; hint.className='nat-hint warn'; hint.textContent=g.natNote; }
    else if(g&&g.natInvalid){
      hint.hidden=false; hint.className='nat-hint bad';
      const chips=(g.natCandidates||[]).map(c=>`<button type="button" class="nat-cand" data-code="${c}">${c} — ${escapeHtml(countryByCode.get(c)||'')}</button>`).join('');
      hint.innerHTML=`Mã <b>${escapeHtml(g.nationality)}</b> không có trong danh sách. ${chips?'Chọn mã đúng: '+chips:'Nhập mã hợp lệ ở ô trên.'}`;
    } else { hint.hidden=true; hint.innerHTML=''; }
  }
  $('reasonBox').textContent=g?.reasons?.length?`Cần kiểm tra: ${g.reasons.join('; ')}`:'';
}
function renderCounters(){
  const total=guests.length, valid=guests.filter(g=>g.status==='ok').length, review=guests.filter(g=>g.status==='review').length, vn=guests.filter(g=>g.status==='excluded').length;
  const selected=guests.filter(g=>g.selected).length;
  for(const [id,v] of Object.entries({totalCount:total,validCount:valid,reviewCount:review,vnCount:vn,metricTotal:total,metricValid:valid,metricReview:review,metricVn:vn})) $(id).textContent=v;
  $('selectedText').textContent=`Đã chọn: ${selected} / ${total} dòng`;
  $('selectAll').checked=total>0 && guests.filter(g=>g.status!=='excluded').every(g=>g.selected);
}
function findGuest(id){ return guests.find(g=>g.id===id); }
function updateGuestField(g,field,value){
  if(field==='fullName') g[field]=normalizeName(value);
  else if(field==='birthDate'){ const d=parseDateValue(value,true); g.birthDate=d.value; if(d.precision) g.birthPrecision=d.precision; }
  else if(field==='birthPrecision') g[field]=String(value).charAt(0).toUpperCase();
  else if(field==='gender') g[field]=normalizeGender(value);
  else if(field==='nationality'){ g.nationality=normalizeNationality(value); g.natNote=''; }
  else if(field==='passport') g[field]=normalizePassport(value);
  else if(field==='room') g[field]=normalizeRoom(value);
  else if(['arrival','departure','checkout'].includes(field)) g[field]=parseDateValue(value,false).value;
  else if(field==='forceReview') g[field]=!!value;
  else g[field]=cleanText(value);
}


function pasteGridData(startId,startField,text){
  const visible=filteredGuests();
  let startRow=visible.findIndex(g=>g.id===startId);
  const startCol=FIELDS.indexOf(startField);
  if(startRow<0||startCol<0)return;
  const lines=text.replace(/\r/g,'').split('\n');
  while(lines.length&&lines[lines.length-1]==='')lines.pop();
  let changed=0;
  lines.forEach((line,ri)=>{
    const cells=line.split('\t');
    let g=visible[startRow+ri];
    if(!g){
      g=makeGuest({}, {name:'Dán từ Excel',type:'paste'});
      guests.push(g); visible.push(g);
    }
    cells.forEach((value,ci)=>{
      const field=FIELDS[startCol+ci];
      if(!field)return;
      updateGuestField(g,field,value); changed++;
    });
  });
  validateAll(); renderAll();
  toast(`Đã dán ${changed} ô dữ liệu`,'ok');
}

const QUICK_FILL_FIELDS=[
  ['passport','Số hộ chiếu'],['nationality','Quốc tịch'],['room','Số phòng'],
  ['arrival','Ngày đến'],['departure','Ngày đi dự kiến'],['checkout','Ngày trả phòng'],
  ['gender','Giới tính'],['birthPrecision','Ngày sinh đúng đến']
];
function openQuickFill(){
  const available=guests.filter(g=>g.status!=='excluded');
  if(!available.length)return toast('Chưa có khách nước ngoài để điền','bad');
  const selected=available.filter(g=>g.selected).length;
  $('modalTitle').textContent='Điền nhanh cho đoàn';
  $('modalContent').innerHTML=`<div class="quick-fill-grid">
    <label for="quickField">Cột cần điền</label>
    <select id="quickField">${QUICK_FILL_FIELDS.map(([v,l])=>`<option value="${v}">${l}</option>`).join('')}</select>
    <label for="quickScope">Phạm vi</label>
    <select id="quickScope"><option value="selected">Các dòng đang chọn (${selected})</option><option value="all">Tất cả khách nước ngoài (${available.length})</option></select>
    <label for="quickValues">Giá trị</label>
    <textarea id="quickValues" placeholder="Dán một cột từ Excel: mỗi khách một dòng.\nNếu chỉ nhập một giá trị, app áp dụng cho tất cả dòng trong phạm vi."></textarea>
    <div></div><div class="quick-options"><label><input type="checkbox" id="quickOnlyBlank" checked> Chỉ điền các ô đang trống</label><button type="button" class="quick-today" id="quickToday">Dùng ngày hôm nay</button></div>
    <div class="quick-note">Số hộ chiếu khác nhau: dán danh sách theo đúng thứ tự dòng. Các thông tin giống nhau của cả đoàn như quốc tịch hoặc ngày đến: chỉ cần nhập một lần.</div>
    <div></div><div class="quick-options"><button type="button" class="quick-today" id="replaceSlashBtn">Thay / bằng khoảng trắng trong Họ tên</button><button type="button" class="quick-today" id="removeArrivalOnlyBtn">Xóa dòng chỉ có Ngày đến</button></div>
  </div>`;
  $('modalActions').innerHTML='<button id="quickCancel" style="background:#7a8794;margin-right:8px">Quay lại</button><button id="quickApply">Áp dụng</button>';
  $('modal').classList.remove('hidden');
  $('quickCancel').onclick=closeModal;
  $('quickToday').onclick=()=>{$('quickValues').value=todayDate();$('quickValues').focus();};
  $('replaceSlashBtn').onclick=()=>{let n=0;guests.filter(g=>g.status!=='excluded'&&g.selected).forEach(g=>{const v=g.fullName.replace(/\s*\/\s*/g,' ').replace(/\s+/g,' ').trim();if(v!==g.fullName){g.fullName=v;n++;}});renderAll();toast(`Đã chuẩn hóa ${n} họ tên`,'ok');};
  $('removeArrivalOnlyBtn').onclick=()=>{const before=guests.length;guests=guests.filter(g=>!isBlankOrArrivalOnly(g));if(!findGuest(activeId))activeId=guests[0]?.id||null;renderAll();toast(`Đã xóa ${before-guests.length} dòng trống/chỉ có Ngày đến`,'ok');};
  $('quickApply').onclick=applyQuickFill;
  setTimeout(()=>$('quickValues')?.focus(),0);
}
function applyQuickFill(){
  const field=$('quickField').value;
  const scope=$('quickScope').value;
  const onlyBlank=$('quickOnlyBlank').checked;
  let targets=guests.filter(g=>g.status!=='excluded'&&(scope==='all'||g.selected));
  if(onlyBlank)targets=targets.filter(g=>!cleanText(g[field]));
  if(!targets.length)return toast('Không có ô phù hợp để điền','bad');
  const raw=$('quickValues').value.replace(/\r/g,'');
  let values=raw.split('\n');
  while(values.length&&values[values.length-1].trim()==='')values.pop();
  if(!values.length)return toast('Hãy nhập hoặc dán dữ liệu cần điền','bad');
  let applied=0;
  if(values.length===1){
    targets.forEach(g=>{updateGuestField(g,field,values[0]);applied++;});
  }else{
    const count=Math.min(values.length,targets.length);
    for(let i=0;i<count;i++){
      if(values[i].trim()==='')continue;
      updateGuestField(targets[i],field,values[i]);applied++;
    }
  }
  validateAll(); renderAll(); closeModal();
  const extra=values.length>1&&values.length!==targets.length?` (${values.length} giá trị / ${targets.length} dòng)`:'';
  toast(`Đã điền ${applied} ô${extra}`,'ok');
}

async function loadReferenceData(){
  const builtins={
    fullName:['guest name','name','full name','ho ten','họ tên','guest','passenger name','customer name','ten khach','tên khách'],
    birthDate:['date of birth','birth date','birthday','dob','ngay sinh','ngày sinh'],
    birthPrecision:['birth precision','ngay sinh dung den','ngày sinh đúng đến','precision'],
    gender:['gender','sex','gioi tinh','giới tính'],
    nationality:['nationality','country','nationality code','country code','quoc tich','quốc tịch','ma quoc tich','mã quốc tịch'],
    passport:['passport','passport no','passport number','passport #','document no','travel document','so ho chieu','số hộ chiếu'],
    room:['room','room no','room number','room #','so phong','số phòng'],
    arrival:['arrival','check in','check-in','arrival date','ngay den','ngày đến'],
    departure:['departure','check out','check-out','expected departure','ngay di','ngày đi','ngay di du kien','ngày đi dự kiến'],
    checkout:['actual departure','checkout date','check out date','ngay tra phong','ngày trả phòng']
  };
  for(const [field,arr] of Object.entries(builtins)) for(const a of arr) aliasMap.set(normalizeKey(a),field);
  try{
    const info=await (await fetch('/api/reference',{cache:'no-store'})).json();
    for(const [code,label] of Object.entries(info.countries||{})){countryByCode.set(code,label);countryNameToCode.set(normalizeKey(label),code);}
    buildNationalityList();
  }catch(e){console.warn('Reference load failed',e);}
}
// Fill the nationality <datalist> so the field suggests "CODE - Name"; typing a
// code or a country name both surface the match. normalizeNationality() resolves
// whatever the user picks/types back to the 3-letter export code.
function buildNationalityList(){
  const dl=$('natList'); if(!dl) return;
  dl.innerHTML=[...countryByCode.entries()].sort((a,b)=>a[0].localeCompare(b[0]))
    .map(([code,name])=>`<option value="${escapeHtml(code+' - '+name)}"></option>`).join('');
}

function detectHeader(rows){
  let best={index:-1,score:0,map:{}};
  for(let i=0;i<Math.min(rows.length,25);i++){
    const map={}; let score=0;
    rows[i].forEach((cell,col)=>{ const f=aliasMap.get(normalizeKey(cell)); if(f && map[f]===undefined){map[f]=col;score++;} });
    if(score>best.score) best={index:i,score,map};
  }
  return best;
}
function recordFromMappedRow(row,map){
  const get=f=>map[f]===undefined?'':row[map[f]];
  return {fullName:get('fullName'),birthDate:get('birthDate'),birthPrecision:get('birthPrecision'),gender:get('gender'),nationality:get('nationality'),passport:get('passport'),room:get('room'),arrival:get('arrival'),departure:get('departure'),checkout:get('checkout')};
}
function recordsFromMatrix(rows){
  const header=detectHeader(rows);
  if(header.score>=3){
    return rows.slice(header.index+1).map(r=>recordFromMappedRow(r,header.map)).filter(r=>Object.values(r).some(v=>cleanText(v)));
  }
  return recordsFromTextLines(rows.map(r=>r.map(cleanText).filter(Boolean).join('   ')));
}

async function filePreview(file){
  if(!file.type.startsWith('image/')) return '';
  return await new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(r.result);r.onerror=reject;r.readAsDataURL(file);});
}
async function uploadImport(file,mode){
  const fd=new FormData(); fd.append('file',file,file.name); fd.append('mode',mode);
  const res=await fetch('/api/import',{method:'POST',body:fd});
  let data={}; try{data=await res.json();}catch{}
  if(!res.ok) throw new Error(data.error||`Lỗi máy xử lý (${res.status})`);
  return data;
}
function applyBackendResult(data,file,type,fallbackPreview=''){
  const records=Array.isArray(data.records)?data.records:[];
  const preview=data.preview||fallbackPreview||'';
  const n=addGuests(records,{name:file.name,type,preview,confidence:data.confidence??null});
  if(data.warning) toast(data.warning,n?'':'bad');
  return n;
}

async function processExcel(file){
  setBusy(true,'Đang đọc Excel',file.name,10);
  try{const data=await uploadImport(file,'excel');applyBackendResult(data,file,'excel');}
  catch(e){console.error(e);showError(`Không đọc được file Excel: ${e.message}`);} finally{setBusy(false);}
}

function groupPdfLines(items){
  const groups=[];
  for(const it of items){
    const y=Math.round(it.transform?.[5]||0); let g=groups.find(x=>Math.abs(x.y-y)<=3);
    if(!g){g={y,items:[]};groups.push(g);} g.items.push({text:cleanText(it.str),x:it.transform?.[4]||0,w:it.width||0});
  }
  return groups.sort((a,b)=>b.y-a.y).map(g=>{
    g.items.sort((a,b)=>a.x-b.x); let text=''; let prev=null;
    for(const it of g.items){ if(!it.text)continue; if(prev){const gap=it.x-(prev.x+prev.w);text+=gap>18?'   ':' ';} text+=it.text;prev=it; }
    return text.trim();
  }).filter(Boolean);
}
async function processPdf(file){
  setBusy(true,'Đang đọc PDF bằng engine TBLT',file.name,5);
  try{const data=await uploadImport(file,'pdf');applyBackendResult(data,file,'pdf');}
  catch(e){console.error(e);showError(`Không đọc được PDF: ${e.message}`);} finally{setBusy(false);}
}

function loadImage(file){
  return new Promise((resolve,reject)=>{ const r=new FileReader();r.onload=()=>{const img=new Image();img.onload=()=>resolve({img,dataUrl:r.result});img.onerror=reject;img.src=r.result;};r.onerror=reject;r.readAsDataURL(file); });
}
function imageToCanvas(img){ const c=document.createElement('canvas');c.width=img.naturalWidth;c.height=img.naturalHeight;c.getContext('2d').drawImage(img,0,0);return c; }
function preprocessCanvas(source,targetWidth=2200,crop=null){
  const sx=crop?.x??0, sy=crop?.y??0, sw=crop?.w??source.width, sh=crop?.h??source.height;
  const scale=Math.min(3,Math.max(1,targetWidth/sw)); const c=document.createElement('canvas');c.width=Math.round(sw*scale);c.height=Math.round(sh*scale);
  const ctx=c.getContext('2d',{willReadFrequently:true});ctx.drawImage(source,sx,sy,sw,sh,0,0,c.width,c.height);
  const im=ctx.getImageData(0,0,c.width,c.height),d=im.data;
  for(let i=0;i<d.length;i+=4){ const gray=.299*d[i]+.587*d[i+1]+.114*d[i+2]; const v=Math.max(0,Math.min(255,(gray-128)*1.45+128)); d[i]=d[i+1]=d[i+2]=v; }
  ctx.putImageData(im,0,0); return c;
}
async function getGenericWorker(){
  if(genericWorker) return genericWorker;
  genericWorker=await Tesseract.createWorker(settings.ocrVietnamese?['eng','vie']:['eng'],1,{
    workerPath:new URL('./vendor/worker.min.js', import.meta.url).href,corePath:new URL('./vendor/tesseract-core', import.meta.url).href,langPath:new URL('./vendor/lang', import.meta.url).href,
    logger:m=>{ if(m.progress!==undefined) setBusy(true,'Đang nhận diện bảng',m.status,Math.round(m.progress*100)); }
  });
  await genericWorker.setParameters({tessedit_pageseg_mode:Tesseract.PSM.AUTO,preserve_interword_spaces:'1'});
  return genericWorker;
}
async function getMrzWorker(){
  if(mrzWorker) return mrzWorker;
  mrzWorker=await Tesseract.createWorker('eng',1,{workerPath:new URL('./vendor/worker.min.js', import.meta.url).href,corePath:new URL('./vendor/tesseract-core', import.meta.url).href,langPath:new URL('./vendor/lang', import.meta.url).href,logger:m=>{if(m.progress!==undefined)setBusy(true,'Đang đọc MRZ',m.status,Math.round(m.progress*100));}});
  await mrzWorker.setParameters({tessedit_pageseg_mode:Tesseract.PSM.SINGLE_BLOCK,tessedit_char_whitelist:'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789<',preserve_interword_spaces:'1'});
  return mrzWorker;
}
async function recognizeGeneric(canvas){
  const w=await getGenericWorker(); const result=await w.recognize(canvas,{}, {text:true,blocks:true,tsv:true}); return result.data;
}
function recordsFromOcrData(data){
  const lines=[];
  if(data.blocks){
    for(const b of data.blocks||[]) for(const p of b.paragraphs||[]) for(const l of p.lines||[]){
      const text=(l.words||[]).map(w=>w.text).join(' ').trim(); if(text) lines.push(text);
    }
  }
  if(!lines.length) lines.push(...String(data.text||'').split(/\r?\n/));
  return recordsFromTextLines(lines);
}
async function processTableImage(file){
  setBusy(true,'Đang nhận diện ảnh bảng',file.name,8);
  try{const preview=await filePreview(file);const data=await uploadImport(file,'table');applyBackendResult(data,file,'table-image',preview);}
  catch(e){console.error(e);showError(`Không đọc được ảnh bảng: ${e.message}`);} finally{setBusy(false);}
}

function parseMrz(text){
  let lines=String(text||'').toUpperCase().split(/\r?\n/).map(s=>s.replace(/[^A-Z0-9<]/g,'')).filter(s=>s.length>=25);
  if(lines.length<2){
    const compact=String(text||'').toUpperCase().replace(/[^A-Z0-9<\n]/g,''); lines=compact.split(/\n/).filter(s=>s.length>=25);
  }
  let l1=lines.find(s=>s.startsWith('P<'))||lines[lines.length-2]||'';
  let l2=lines.find((s,i)=>i>0 && /\d/.test(s) && s!==l1)||lines[lines.length-1]||'';
  l1=l1.padEnd(44,'<').slice(0,44); l2=l2.padEnd(44,'<').slice(0,44);
  if(!l1.startsWith('P') || l2.length<30) return null;
  const names=l1.slice(5).split('<<'); const surname=(names[0]||'').replace(/<+/g,' ').trim(); const given=(names.slice(1).join(' ')||'').replace(/<+/g,' ').trim();
  const rawBirth=l2.slice(13,19); let birth='';
  if(/^\d{6}$/.test(rawBirth)){ const yy=Number(rawBirth.slice(0,2)),mm=rawBirth.slice(2,4),dd=rawBirth.slice(4,6),nowYY=new Date().getFullYear()%100; const yyyy=(yy>nowYY?1900:2000)+yy;birth=`${dd}/${mm}/${yyyy}`; }
  const sex=l2.charAt(20); const nationality=l2.slice(10,13).replace(/</g,'');
  return {fullName:cleanText(`${surname} ${given}`),birthDate:birth,birthPrecision:'D',gender:sex==='M'?'M':sex==='F'?'F':'',nationality,passport:l2.slice(0,9).replace(/</g,''),room:'',arrival:'',departure:'',checkout:''};
}
async function processPassport(file){
  setBusy(true,'Dò và dựng thẳng hộ chiếu',file.name,5);
  try{
    const originalPreview=await filePreview(file);
    const data=await uploadImport(file,'passport');
    applyBackendResult(data,file,'passport',originalPreview);
  }catch(e){console.error(e);showError(`Không đọc được hộ chiếu: ${e.message}`);} finally{setBusy(false);}
}

// Turn a clipboard image (kept only in memory) into exactly one passport guest.
// The blob is wrapped as a File and sent through the same passport pipeline;
// it is never written to disk as a JPG on the app side.
async function importPassportImageBlob(blob){
  if(!blob){ toast('Không tìm thấy ảnh trong clipboard.','bad'); return; }
  const type=blob.type||'image/png';
  const ext=type.includes('png')?'png':type.includes('jpeg')||type.includes('jpg')?'jpg':type.includes('bmp')?'bmp':'png';
  const stamp=new Date().toISOString().replace(/[:.]/g,'').replace('T','_').slice(0,15);
  const file=new File([blob],`paste-${stamp}.${ext}`,{type});
  await processPassport(file);
}
// Read the first image on the clipboard via the async Clipboard API (button).
async function pastePassportFromClipboard(){
  try{
    if(!navigator.clipboard||!navigator.clipboard.read){ toast('Không tìm thấy ảnh trong clipboard.','bad'); return; }
    const items=await navigator.clipboard.read();
    for(const item of items){
      const type=(item.types||[]).find(t=>t.startsWith('image/'));
      if(type){ const blob=await item.getType(type); await importPassportImageBlob(blob); return; }
    }
    toast('Không tìm thấy ảnh trong clipboard.','bad');
  }catch(e){ console.error(e); toast('Không tìm thấy ảnh trong clipboard.','bad'); }
}

function isHeaderLine(line){
  const n=normalizeKey(line); let hits=0;
  for(const k of ['ho ten','name','ngay sinh','birth','gender','gioi tinh','nationality','quoc tich','passport','ho chieu','room','phong','arrival','departure','check in','check out']) if(n.includes(k))hits++;
  return hits>=2;
}
function recordsFromTextLines(lines){
  const out=[];
  for(let raw of lines){
    raw=cleanText(raw); if(!raw||raw.length<5||isHeaderLine(raw))continue;
    const rec=parseGuestLine(raw); if(rec)out.push(rec);
  }
  return out;
}
function parseGuestLine(line){
  const dateRx=/(?:\b\d{1,2}[\/\.\-]\d{1,2}[\/\.\-](?:\d{2}|\d{4})\b|\b\d{1,2}[\/\.\-]\d{4}\b|\b(?:19|20)\d{2}\b)/g;
  const matches=[...line.matchAll(dateRx)]; if(!matches.length)return null;
  const first=matches[0]; let before=line.slice(0,first.index).trim().replace(/^\d{1,4}[.)\-]?\s+/,'');
  if(before.split(/\s+/).length<1 || !/[A-Za-zÀ-ỹ]/.test(before))return null;
  const dates=matches.map(m=>parseDateValue(m[0],true));
  const afterFirstStart=first.index+first[0].length; const afterFirstEnd=matches[1]?.index??line.length; const meta=line.slice(afterFirstStart,afterFirstEnd).trim();
  let tokens=meta.split(/\s+/).map(t=>t.replace(/^[^A-Za-z0-9À-ỹ]+|[^A-Za-z0-9À-ỹ]+$/g,'')).filter(Boolean);
  let gender=''; const gi=tokens.findIndex(t=>normalizeGender(t)); if(gi>=0){gender=normalizeGender(tokens[gi]);tokens.splice(gi,1);}
  let nationality=''; const ni=tokens.findIndex(t=>{const u=t.toUpperCase();return /^[A-Z]{3}$/.test(u)&&countryByCode.has(u);}); if(ni>=0){nationality=tokens[ni].toUpperCase();tokens.splice(ni,1);}
  let passport='',room='';
  const candidates=tokens.filter(t=>/^[A-Za-z0-9\-]{2,14}$/.test(t));
  if(candidates.length){
    let pi=candidates.findIndex(t=>/\d/.test(t)&&t.replace(/\W/g,'').length>=6); if(pi<0)pi=0;
    passport=normalizePassport(candidates[pi]); const rest=candidates.filter((_,i)=>i!==pi);
    room=rest.length?normalizeRoom(rest[rest.length-1]):'';
    if(passport.length<=5 && room.length>passport.length){const tmp=passport;passport=room;room=tmp;}
  }
  return {fullName:before,birthDate:dates[0]?.value||'',birthPrecision:dates[0]?.precision||'D',gender,nationality,passport,room,arrival:dates[1]?.value||'',departure:dates[2]?.value||'',checkout:dates[3]?.value||''};
}

function isBlankOrArrivalOnly(g){
  return !cleanText(g.fullName)&&!cleanText(g.birthDate)&&!cleanText(g.gender)&&!cleanText(g.nationality)&&!cleanText(g.passport)&&!cleanText(g.room)&&!cleanText(g.departure)&&!cleanText(g.checkout);
}
function exportableRows(){ return guests.filter(g=>g.selected&&g.status!=='excluded'&&(!settings.skipBlankArrivalOnly||!isBlankOrArrivalOnly(g))); }
async function confirmExport(rows){
  if(!rows.length){ showError('Chưa chọn dòng khách nước ngoài nào để xuất.'); return false; }
  // Hard block: no export while any selected row has an invalid nationality code.
  const badNat=rows.filter(g=>g.nationality && countryByCode.size && !countryByCode.has(g.nationality));
  if(badNat.length){
    showModal('Không thể xuất',`<div class="warning-box">Còn <b>${badNat.length}</b> dòng có <b>mã quốc tịch không hợp lệ</b> (không có trong danh sách mã của file mẫu). Hãy sửa (chọn mã gần đúng trong khung chi tiết) trước khi xuất.</div><ul>${badNat.map(g=>`<li>${escapeHtml(g.fullName||'(chưa có tên)')}: <b>${escapeHtml(g.nationality)}</b></li>`).join('')}</ul>`);
    return false;
  }
  const incomplete=rows.filter(g=>g.status!=='ok');
  if(incomplete.length){
    return await askConfirm('Xuất dữ liệu còn thiếu',`<div class="warning-box">Có <b>${incomplete.length}</b> / <b>${rows.length}</b> dòng còn thiếu hoặc cần kiểm tra. App vẫn xuất toàn bộ các dòng đã chọn và giữ trống dữ liệu chưa có để anh chỉnh sau.</div><p>Khách Việt Nam luôn được loại khỏi file.</p>`,'Vẫn xuất');
  }
  return true;
}
function timestampName(ext){ const d=new Date(); const p=n=>String(n).padStart(2,'0');return `XNC_${d.getFullYear()}${p(d.getMonth()+1)}${p(d.getDate())}_${p(d.getHours())}${p(d.getMinutes())}.${ext}`; }
function downloadBlob(blob,name){ const a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download=name;document.body.appendChild(a);a.click();setTimeout(()=>{URL.revokeObjectURL(a.href);a.remove();},1500); }
async function exportXml(){
  const rows=exportableRows(); if(!await confirmExport(rows))return;
  const nodes=rows.map((g,i)=>`    <THONG_TIN_KHACH>\n        <so_thu_tu>${i+1}</so_thu_tu>\n        <ho_ten>${xmlEscape(g.fullName)}</ho_ten>\n        <ngay_sinh>${xmlEscape(g.birthDate)}</ngay_sinh>\n        <ngay_sinh_dung_den>${g.birthPrecision}</ngay_sinh_dung_den>\n        <gioi_tinh>${g.gender}</gioi_tinh>\n        <ma_quoc_tich>${g.nationality}</ma_quoc_tich>\n        <so_ho_chieu>${xmlEscape(g.passport)}</so_ho_chieu>\n        <so_phong>${xmlEscape(g.room)}</so_phong>\n        <ngay_den>${g.arrival}</ngay_den>\n        <ngay_di_du_kien>${g.departure}</ngay_di_du_kien>\n        <ngay_tra_phong>${g.checkout}</ngay_tra_phong>\n    </THONG_TIN_KHACH>`).join('\n');
  const xml=`<?xml version="1.0" encoding="UTF-8"?>\n<KHAI_BAO_TAM_TRU>\n${nodes}\n</KHAI_BAO_TAM_TRU>`;
  downloadBlob(new Blob([xml],{type:'application/xml;charset=utf-8'}),timestampName('xml')); setStatus(`Đã xuất XML: ${rows.length} khách`); toast('Xuất XML thành công','ok');
}
async function exportExcel(){
  const rows=exportableRows(); if(!await confirmExport(rows))return;
  setBusy(true,'Đang tạo Excel','Giữ nguyên mẫu chính thức',20);
  try{
    const res=await fetch('/api/export/excel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({rows})});
    if(!res.ok){let err='';try{err=(await res.json()).error}catch{}throw new Error(err||`HTTP ${res.status}`);}
    const blob=await res.blob();downloadBlob(blob,timestampName('xlsx'));
    setStatus(`Đã xuất Excel: ${rows.length} khách`);toast('Xuất Excel thành công','ok');
  }catch(e){console.error(e);showError(`Không tạo được Excel: ${e.message}`);} finally{setBusy(false);}
}

function setBusy(show,title='',detail='',progress=0){
  $('busyOverlay').classList.toggle('hidden',!show); if(show){$('busyTitle').textContent=title;$('busyDetail').textContent=detail;$('progressBar').style.width=`${Math.max(0,Math.min(100,progress))}%`;setStatus(title);}
  else setStatus('Sẵn sàng');
}
function setStatus(text){$('statusText').textContent=text;}
function toast(text,type=''){ let t=document.querySelector('.toast');if(!t){t=document.createElement('div');t.className='toast';document.body.appendChild(t);}t.textContent=text;t.className=`toast ${type}`;requestAnimationFrame(()=>t.classList.add('show'));setTimeout(()=>t.classList.remove('show'),2800);}
function showModal(title,html){$('modalTitle').textContent=title;$('modalContent').innerHTML=html;$('modalActions').innerHTML='<button id="modalOk">Đóng</button>';$('modal').classList.remove('hidden');$('modalOk').onclick=closeModal;}
function closeModal(){$('modal').classList.add('hidden');}
function showError(text){showModal('Không thể thực hiện',`<div class="warning-box">${escapeHtml(text)}</div>`);}
function askConfirm(title,html,confirmText='Tiếp tục'){
  return new Promise(resolve=>{ $('modalTitle').textContent=title;$('modalContent').innerHTML=html;$('modalActions').innerHTML=`<button id="cancelAsk" style="background:#7a8794;margin-right:8px">Quay lại</button><button id="confirmAsk">${escapeHtml(confirmText)}</button>`;$('modal').classList.remove('hidden');
    $('cancelAsk').onclick=()=>{closeModal();resolve(false)};$('confirmAsk').onclick=()=>{closeModal();resolve(true)};
  });
}

async function processWord(file){
  setBusy(true,'Đang đọc Word',file.name,8);
  try{const data=await uploadImport(file,'word');applyBackendResult(data,file,'word');}
  catch(e){console.error(e);showError(`Không đọc được Word: ${e.message}`);} finally{setBusy(false);}
}

async function processFiles(files,mode='auto'){
  for(const file of files){
    const ext=file.name.split('.').pop().toLowerCase();
    if(mode==='passport') await processPassport(file);
    else if(mode==='table-image') await processTableImage(file);
    else if(mode==='pdf'||ext==='pdf') await processPdf(file);
    else if(mode==='word'||['docx','doc'].includes(ext)) await processWord(file);
    else if(mode==='excel'||['xlsx','xls','csv'].includes(ext)) await processExcel(file);
    else if(['jpg','jpeg','png'].includes(ext)) await processTableImage(file);
    else toast(`Không hỗ trợ định dạng: ${file.name}`,'bad');
  }
}

// Bản offline: không có AI. Toàn bộ dữ liệu xử lý trực tiếp trên máy.
function openSettings(){
  showModal('Cài đặt',`<div class="help-list">
    <label><input type="checkbox" id="setUpper" ${settings.autoUppercase?'checked':''}> Tự chuyển họ tên thành chữ in hoa</label><br>
    <label><input type="checkbox" id="setCopy" ${settings.copyDepartureToCheckout?'checked':''}> Khi thiếu, dùng Ngày đi dự kiến làm Ngày trả phòng</label><br>
    <label><input type="checkbox" id="setVie" ${settings.ocrVietnamese?'checked':''}> OCR cả tiêu đề tiếng Việt (khuyến nghị)</label><br>
    <label><input type="checkbox" id="setSkipBlank" ${settings.skipBlankArrivalOnly?'checked':''}> Tự bỏ qua dòng trống hoặc chỉ có Ngày đến khi xuất</label>
    <hr>
    <div class="about-box"><img src="./assets/logo.svg"><div><b>XNC - Khai báo tạm trú khách nước ngoài</b><br>Version 1.0.14<br>Developed by Ocean<br>© 2026 Ocean<br><small>Dữ liệu được xử lý trực tiếp trên máy, không tải lên máy chủ.</small></div></div>
  </div>`);
  setTimeout(()=>{
    $('setUpper').onchange=e=>{settings.autoUppercase=e.target.checked;saveSettings()};
    $('setCopy').onchange=e=>{settings.copyDepartureToCheckout=e.target.checked;saveSettings()};
    $('setVie').onchange=e=>{settings.ocrVietnamese=e.target.checked;saveSettings();};
    $('setSkipBlank').onchange=e=>{settings.skipBlankArrivalOnly=e.target.checked;saveSettings();};
  },0);
}
// ===== Passport image viewer, splitter, quick-edit actions =====
let inlineViewer=null, bigViewer=null, detailW=0;
let cropMode=false, cropRectLocal=null, cropDrag=null;

// makeViewer wires zoom (wheel), pan (drag), Fit/100%/200%, rotate and an MRZ-band
// zoom onto one <img> inside a stage element. State is kept per instance so the
// inline pane and the full-screen overlay are independent.
function makeViewer(stage, img){
  const st={scale:1,tx:0,ty:0,rot:0,nw:0,nh:0};
  const rsize=()=>{ const r=((st.rot%360)+360)%360; return (r===90||r===270)?[st.nh,st.nw]:[st.nw,st.nh]; };
  const apply=()=>{ img.style.transform=`translate(-50%,-50%) translate(${st.tx}px,${st.ty}px) rotate(${st.rot}deg) scale(${st.scale})`; };
  function fit(){ const b=stage.getBoundingClientRect(); const [w,h]=rsize(); st.scale=(w&&h)?Math.min(b.width/w,b.height/h)*0.97:1; st.tx=0; st.ty=0; apply(); }
  function zoom(z){ st.scale=z; st.tx=0; st.ty=0; apply(); }
  function mrz(){ const b=stage.getBoundingClientRect(); const [w,h]=rsize(); if(!w||!h)return; const band=0.26; st.scale=Math.min(b.width/w,b.height/(h*band))*0.97; st.tx=0; st.ty=-h*(0.5-band/2)*st.scale; apply(); }
  function rotate(d){ st.rot=(st.rot+d+360)%360; fit(); }
  function load(src){
    if(!src){ img.hidden=true; img.removeAttribute('src'); return; }
    img.hidden=false;
    const done=()=>{ st.nw=img.naturalWidth; st.nh=img.naturalHeight; st.rot=0; fit(); };
    if(img.getAttribute('src')!==src){ img.onload=done; img.setAttribute('src',src); }
    else if(img.complete && img.naturalWidth){ done(); }
  }
  stage.addEventListener('wheel',e=>{ if(img.hidden)return; e.preventDefault(); const f=e.deltaY<0?1.15:1/1.15; st.scale=Math.max(.05,Math.min(25,st.scale*f)); apply(); },{passive:false});
  let drag=null;
  stage.addEventListener('mousedown',e=>{ if(img.hidden||stage.classList.contains('cropping'))return; drag={x:e.clientX,y:e.clientY,tx:st.tx,ty:st.ty}; stage.classList.add('grabbing'); });
  window.addEventListener('mousemove',e=>{ if(!drag)return; st.tx=drag.tx+(e.clientX-drag.x); st.ty=drag.ty+(e.clientY-drag.y); apply(); });
  window.addEventListener('mouseup',()=>{ if(drag){drag=null;stage.classList.remove('grabbing');} });
  return {st,fit,zoom,mrz,rotate,load};
}
function viewerCmd(v,cmd){ if(cmd==='fit')v.fit(); else if(cmd==='z100')v.zoom(1); else if(cmd==='z200')v.zoom(2); else if(cmd==='mrz')v.mrz(); else if(cmd==='rotl')v.rotate(-90); else if(cmd==='rotr')v.rotate(90); }

function openBig(){ const g=findGuest(activeId); if(!g||!g.preview){ toast('Không có ảnh để xem','bad'); return; } $('bigView').classList.remove('hidden'); bigViewer.load(g.preview); requestAnimationFrame(()=>bigViewer.fit()); }
function closeBig(){ $('bigView').classList.add('hidden'); if(document.fullscreenElement) document.exitFullscreen().catch(()=>{}); }
function toggleFullscreen(el){ if(document.fullscreenElement){ document.exitFullscreen().catch(()=>{}); } else { (el||document.documentElement).requestFullscreen().catch(()=>{}); } }

// Splitter: detail pane defaults to ~38% of the workspace width and is draggable.
function setDetailWidth(px){ const ws=$('dropZone'); const total=ws.clientWidth; const min=300,max=Math.max(min,Math.min(total-560,total*0.62)); px=Math.max(min,Math.min(max,px)); ws.style.gridTemplateColumns=`minmax(0,1fr) 6px ${Math.round(px)}px`; detailW=px; }
function initSplitter(){
  const ws=$('dropZone'), sp=$('splitter'); if(!sp)return;
  setDetailWidth(ws.clientWidth*0.38);
  let d=null;
  sp.addEventListener('mousedown',e=>{ d={x:e.clientX,w:detailW}; document.body.classList.add('col-resizing'); e.preventDefault(); });
  window.addEventListener('mousemove',e=>{ if(!d)return; setDetailWidth(d.w-(e.clientX-d.x)); });
  window.addEventListener('mouseup',()=>{ if(d){d=null; document.body.classList.remove('col-resizing'); inlineViewer&&inlineViewer.fit();} });
  window.addEventListener('resize',()=>{ if(detailW){setDetailWidth(detailW); inlineViewer&&inlineViewer.fit();} });
}

// Manual MRZ crop: draw a rectangle over the image, then OCR just that region and
// update the CURRENT guest (never adds a new row).
function startCropMode(){ const g=findGuest(activeId); if(!g||!g.preview){ toast('Không có ảnh để cắt','bad'); return; } cropMode=true; $('viewerStage').classList.add('cropping'); $('cropBar').hidden=false; cropRectLocal=null; $('cropBox').hidden=true; toast('Kéo chọn đúng 2 dòng MRZ rồi bấm “Đọc vùng đã chọn”','');
}
function cancelCrop(){ cropMode=false; const s=$('viewerStage'); if(s)s.classList.remove('cropping'); const bar=$('cropBar'); if(bar)bar.hidden=true; const box=$('cropBox'); if(box)box.hidden=true; cropRectLocal=null; cropDrag=null; }
async function cropRegionToBlob(){
  const v=inlineViewer.st, rect=cropRectLocal; if(!rect||rect.w<8||rect.h<8) return null;
  const sb=$('viewerStage').getBoundingClientRect(), cx=sb.width/2, cy=sb.height/2;
  const r=(-v.rot)*Math.PI/180, cos=Math.cos(r), sin=Math.sin(r);
  const toNat=(px,py)=>{ let dx=(px-cx-v.tx)/v.scale, dy=(py-cy-v.ty)/v.scale; return [v.nw/2+(dx*cos-dy*sin), v.nh/2+(dx*sin+dy*cos)]; };
  const pts=[toNat(rect.x,rect.y),toNat(rect.x+rect.w,rect.y),toNat(rect.x+rect.w,rect.y+rect.h),toNat(rect.x,rect.y+rect.h)];
  const xs=pts.map(p=>p[0]), ys=pts.map(p=>p[1]);
  const x0=Math.max(0,Math.floor(Math.min(...xs))), y0=Math.max(0,Math.floor(Math.min(...ys)));
  const x1=Math.min(v.nw,Math.ceil(Math.max(...xs))), y1=Math.min(v.nh,Math.ceil(Math.max(...ys)));
  const cw=x1-x0, ch=y1-y0; if(cw<8||ch<8) return null;
  const canvas=document.createElement('canvas'); canvas.width=cw; canvas.height=ch;
  canvas.getContext('2d').drawImage($('viewerImg'), x0,y0,cw,ch, 0,0,cw,ch);
  return await new Promise(res=>canvas.toBlob(b=>res(b),'image/png'));
}

function dataURLtoBlob(u){ const [head,b64]=u.split(','); const mime=(head.match(/data:([^;]+)/)||[])[1]||'image/png'; const bin=atob(b64); const arr=new Uint8Array(bin.length); for(let i=0;i<bin.length;i++)arr[i]=bin.charCodeAt(i); return new Blob([arr],{type:mime}); }
// OCR a passport image (whole image or a crop) and merge the passport fields into
// the guest currently selected — this never creates a new guest.
async function applyPassportRead(blob,label){
  const g=findGuest(activeId); if(!g){ toast('Chưa chọn khách','bad'); return; }
  setBusy(true,label||'Đang đọc lại MRZ',g.sourceName||'',10);
  try{
    const ext=blob.type.includes('png')?'png':'jpg';
    const data=await uploadImport(new File([blob],`reread.${ext}`,{type:blob.type||'image/png'}),'passport');
    const rec=(data.records||[])[0];
    if(!rec){ toast(data.warning||'Không đọc được MRZ từ ảnh này','bad'); return; }
    const norm=makeGuest(rec,{});
    const changed=[];
    for(const f of ['fullName','birthDate','birthPrecision','gender','nationality','passport']){ if(cleanText(norm[f])){ g[f]=norm[f]; changed.push(f); } }
    validateAll(); renderAll();
    toast(changed.length?`Đã cập nhật: ${changed.map(f=>FIELD_LABELS[f]||f).join(', ')}`:'Không có trường nào thay đổi', changed.length?'ok':'');
  }catch(e){ console.error(e); showError('Không đọc được MRZ: '+e.message); }
  finally{ setBusy(false); }
}
async function rereadMrz(){ const g=findGuest(activeId); if(!g||!g.preview){ toast('Khách này không có ảnh nguồn','bad'); return; } await applyPassportRead(dataURLtoBlob(g.preview),'Đang đọc lại MRZ'); }
async function readCroppedRegion(){ const blob=await cropRegionToBlob(); if(!blob){ toast('Vùng chọn quá nhỏ','bad'); return; } cancelCrop(); await applyPassportRead(blob,'Đang đọc vùng đã cắt'); }

function saveNext(){ const list=filteredGuests(); if(!list.length)return; const i=list.findIndex(g=>g.id===activeId); const next=list[i+1]||list[i]||list[0]; activeId=next.id; renderAll(); const f=document.querySelector('#detailForm input[data-field="fullName"]'); if(f)f.focus(); }
function deleteActiveRow(){ const g=findGuest(activeId); if(!g)return; const list=filteredGuests(); const i=list.findIndex(x=>x.id===activeId); guests=guests.filter(x=>x.id!==activeId); const nl=filteredGuests(); activeId=(nl[i]||nl[i-1]||nl[nl.length-1]||{}).id||null; renderAll(); }
function markReviewedActive(){ const g=findGuest(activeId); if(!g)return; g.forceReview=false; validateAll(); renderAll(); toast('Đã đánh dấu đã kiểm tra','ok'); }

function initDetailPane(){
  inlineViewer=makeViewer($('viewerStage'),$('viewerImg'));
  bigViewer=makeViewer($('bigStage'),$('bigImg'));
  // Viewer toolbars
  document.querySelectorAll('#viewer .viewer-toolbar button[data-vz]').forEach(b=>b.onclick=()=>{
    const c=b.dataset.vz;
    if(c==='big') openBig(); else if(c==='crop') startCropMode(); else viewerCmd(inlineViewer,c);
  });
  document.querySelectorAll('#bigView .bigview-toolbar button[data-bz]').forEach(b=>b.onclick=()=>{
    const c=b.dataset.bz;
    if(c==='close') closeBig(); else if(c==='full') toggleFullscreen($('bigView')); else viewerCmd(bigViewer,c);
  });
  $('viewerImg').addEventListener('dblclick',openBig);
  $('fullscreenBtn').onclick=()=>toggleFullscreen($('detailPane'));
  // Crop drag over the stage
  const stage=$('viewerStage');
  stage.addEventListener('mousedown',e=>{ if(!cropMode)return; const sb=stage.getBoundingClientRect(); cropDrag={x:e.clientX-sb.left,y:e.clientY-sb.top}; e.preventDefault(); });
  window.addEventListener('mousemove',e=>{ if(!cropDrag)return; const sb=stage.getBoundingClientRect(); const x=Math.max(0,Math.min(sb.width,e.clientX-sb.left)), y=Math.max(0,Math.min(sb.height,e.clientY-sb.top)); cropRectLocal={x:Math.min(x,cropDrag.x),y:Math.min(y,cropDrag.y),w:Math.abs(x-cropDrag.x),h:Math.abs(y-cropDrag.y)}; const box=$('cropBox'); box.hidden=false; box.style.left=cropRectLocal.x+'px'; box.style.top=cropRectLocal.y+'px'; box.style.width=cropRectLocal.w+'px'; box.style.height=cropRectLocal.h+'px'; });
  window.addEventListener('mouseup',()=>{ cropDrag=null; });
  $('cropRead').onclick=readCroppedRegion;
  $('cropCancel').onclick=cancelCrop;
  // Pick a suggested nationality code (red "invalid" state)
  $('natHint').addEventListener('click',e=>{ const b=e.target.closest('.nat-cand'); if(!b)return; const g=findGuest(activeId); if(!g)return; g.nationality=b.dataset.code; g.natNote=''; validateAll(); renderAll(); });
  // Action buttons
  $('saveNextBtn').onclick=saveNext;
  $('rereadBtn').onclick=rereadMrz;
  $('markReviewedBtn').onclick=markReviewedActive;
  $('deleteRowBtn').onclick=deleteActiveRow;
  // Enter moves to the next field; Ctrl+Enter saves & goes to next guest.
  const fields=[...document.querySelectorAll('#detailForm input:not([type=checkbox]),#detailForm select')];
  fields.forEach((el,i)=>el.addEventListener('keydown',e=>{ if(e.key!=='Enter')return; e.preventDefault(); if(e.ctrlKey){saveNext();return;} const n=fields[i+1]; if(n)n.focus(); else saveNext(); }));
}

function bindEvents(){
  const modeByInput={generalInput:'auto',passportInput:'passport',tableImageInput:'table-image',pdfInput:'pdf',excelInput:'excel',wordInput:'word'};
  document.querySelectorAll('[data-input]').forEach(b=>b.addEventListener('click',()=>$(b.dataset.input).click()));
  for(const [id,mode] of Object.entries(modeByInput)) $(id).addEventListener('change',async e=>{const files=[...e.target.files];e.target.value='';await processFiles(files,mode);});
  $('pastePassportBtn').onclick=pastePassportFromClipboard;
  // Ctrl+V anywhere in the window: if the clipboard holds an image, treat it as a
  // pasted passport (one guest per image); otherwise let the normal text paste run.
  document.addEventListener('paste',async e=>{
    const items=e.clipboardData&&e.clipboardData.items?[...e.clipboardData.items]:[];
    const imgItem=items.find(it=>it.kind==='file'&&it.type&&it.type.startsWith('image/'));
    if(!imgItem) return;
    e.preventDefault();
    await importPassportImageBlob(imgItem.getAsFile());
  });
  const drop=$('dropZone'); ['dragenter','dragover'].forEach(ev=>drop.addEventListener(ev,e=>{e.preventDefault();drop.classList.add('dragging')})); ['dragleave','drop'].forEach(ev=>drop.addEventListener(ev,e=>{e.preventDefault();drop.classList.remove('dragging')})); drop.addEventListener('drop',e=>processFiles([...e.dataTransfer.files],'auto'));
  $('addRowBtn').onclick=()=>{const g=makeGuest({}, {name:'Nhập thủ công',type:'manual'});guests.push(g);activeId=g.id;renderAll();};
  $('quickFillBtn').onclick=openQuickFill;
  $('deleteBtn').onclick=async()=>{const count=guests.filter(g=>g.selected).length;if(!count)return toast('Chưa chọn dòng để xóa','bad');if(await askConfirm('Xóa dữ liệu',`Xóa <b>${count}</b> dòng đã chọn?`,'Xóa')){guests=guests.filter(g=>!g.selected);if(!findGuest(activeId))activeId=guests[0]?.id||null;renderAll();}};
  $('selectAll').onchange=e=>{guests.forEach(g=>{if(g.status!=='excluded')g.selected=e.target.checked});renderAll();};
  $('selectVisible').onchange=e=>{filteredGuests().forEach(g=>{if(g.status!=='excluded')g.selected=e.target.checked});renderAll();};
  $('searchInput').oninput=e=>{currentSearch=e.target.value;renderTable();};
  detailInputs.forEach(el=>el.addEventListener(el.type==='checkbox'||el.tagName==='SELECT'?'change':'input',()=>{const g=findGuest(activeId);if(!g)return;updateGuestField(g,el.dataset.field,el.type==='checkbox'?el.checked:el.value);validateAll();renderTable();renderCounters();$('reasonBox').textContent=g.reasons.length?`Cần kiểm tra: ${g.reasons.join('; ')}`:'';}));
  $('exportXmlBtn').onclick=exportXml;$('exportExcelBtn').onclick=exportExcel;
  $('helpBtn').onclick=()=>showModal('Hướng dẫn sử dụng',`<div class="help-list"><b>1. Thêm dữ liệu</b><br>• Excel: tự nhận diện dòng tiêu đề và ánh xạ cột.<br>• Word: đọc trực tiếp bảng/văn bản; nếu file có ảnh scan, app trích ảnh và OCR.<br>• PDF/ảnh bảng: nhận diện từng dòng khách bằng OCR.<br>• Ảnh hộ chiếu: dò trên ảnh nhỏ, dựng thẳng ảnh gốc, OCR 3 vùng và kiểm tra checksum MRZ; mỗi ảnh tạo đúng một khách.<br>• Đọc chính xác hơn (offline): cài Tesseract-OCR và đặt file <code>mrz.traineddata</code> (hoặc <code>ocrb.traineddata</code>) vào thư mục <code>tessdata</code> — MRZ sẽ đọc bằng model chuyên font OCR-B như máy đọc hộ chiếu.<br>• Dán ảnh hộ chiếu: bấm nút “📋 Dán ảnh hộ chiếu” hoặc nhấn Ctrl+V ở bất kỳ đâu; ảnh chỉ giữ tạm trong bộ nhớ, mỗi lần dán tạo một khách.<br><br><b>2. Kiểm tra</b><br>Ô thiếu hoặc sai được đánh dấu “Cần kiểm tra”. Mã VNM tự bị loại. Có thể sửa trực tiếp trên bảng hoặc khung bên phải.<br><br><b>3. Xuất</b><br>Chọn các dòng cần dùng, sau đó Xuất XML hoặc Xuất Excel. Các dòng còn thiếu vẫn có thể xuất để chỉnh sau; app sẽ cảnh báo trước khi tạo file.<br><br><b>Mẹo</b><br>Ảnh bảng nên chụp thẳng, đủ sáng; ảnh hộ chiếu cần thấy rõ hai dòng MRZ phía dưới.</div>`);
  $('settingsBtn').onclick=openSettings;
  $('modalClose').onclick=closeModal;$('modal').addEventListener('click',e=>{if(e.target===$('modal'))closeModal()});
  document.addEventListener('keydown',e=>{
    if(e.ctrlKey&&e.key.toLowerCase()==='f'){e.preventDefault();$('searchInput').focus();}
    if(e.ctrlKey&&e.key.toLowerCase()==='o'){e.preventDefault();$('generalInput').click();}
    if(e.key==='F11'){ e.preventDefault(); toggleFullscreen($('bigView').classList.contains('hidden')?$('detailPane'):$('bigView')); }
    if(e.key==='Escape'){ if(!$('bigView').classList.contains('hidden')){ closeBig(); } else if(cropMode){ cancelCrop(); } else { closeModal(); } }
  });
  window.addEventListener('beforeunload',()=>{try{navigator.sendBeacon('/api/shutdown','1')}catch{}});
}

(async function init(){
  setBusy(true,'Đang khởi tạo','Nạp mẫu và dữ liệu tham chiếu',15);
  await loadReferenceData(); initDetailPane(); bindEvents(); initSplitter(); renderAll(); setBusy(false);
})();


// XNC_OCEAN_HEARTBEAT_V105

const xncHeartbeat = setInterval(() => {
  fetch('/api/ping', {method:'POST', cache:'no-store', keepalive:true}).catch(()=>{});
}, 3000);
fetch('/api/ping', {method:'POST', cache:'no-store', keepalive:true}).catch(()=>{});
window.addEventListener('pagehide', () => {
  clearInterval(xncHeartbeat);
  try { navigator.sendBeacon('/api/shutdown', new Blob(['1'], {type:'text/plain'})); } catch {}
}, {once:true});
